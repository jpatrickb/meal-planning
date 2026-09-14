package domain

import (
	"database/sql"
	"regexp"
	"strconv"
	"strings"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// unitAliases folds the spellings that turn up in recipe text and receipts
// onto one canonical name each. Recipe ingredient lines are written by hand
// on the recipe site, so "c", "cup" and "cups" all appear for the same unit -
// and an exact-string unit comparison treats them as three incompatible
// units, which is how an item ends up reporting "insufficient stock" while
// sitting on plenty of stock.
var unitAliases = map[string]string{
	"c": "cup", "cups": "cup",
	"tbsp": "Tbsp", "tbsps": "Tbsp", "tablespoon": "Tbsp", "tablespoons": "Tbsp", "t": "Tbsp",
	"tsp": "tsp", "tsps": "tsp", "teaspoon": "tsp", "teaspoons": "tsp",
	"floz": "fl oz", "fluid ounce": "fl oz", "fluid ounces": "fl oz",
	"ounce": "oz", "ounces": "oz", "ozs": "oz",
	"pound": "lb", "pounds": "lb", "lbs": "lb",
	"gram": "g", "grams": "g", "gs": "g",
	"kilogram": "kg", "kilograms": "kg",
	"milliliter": "ml", "milliliters": "ml", "millilitre": "ml", "mls": "ml",
	"liter": "l", "liters": "l", "litre": "l",
	"pints": "pint", "quarts": "quart", "gallons": "gallon",
	"cloves": "clove", "slices": "slice", "sticks": "stick", "strips": "strip",
	"sprigs": "sprig", "stems": "stem", "leaves": "leaf", "bunches": "bunch",
	"cans": "can", "packages": "package", "packets": "packet", "bottles": "bottle",
	"boxes": "box", "bags": "bag", "loaves": "loaf", "jars": "jar",
	"small count": "count", "counts": "count", "each": "count", "whole": "count",
}

// CanonicalUnit folds a unit string onto its canonical spelling. Case is
// preserved through the alias map rather than lowercased outright, because
// "T" and "t" are conventionally tablespoon and teaspoon - but only in that
// exact single-letter form, so everything longer is matched case-insensitively.
func CanonicalUnit(u string) string {
	trimmed := strings.TrimSpace(u)
	if trimmed == "T" {
		return "Tbsp"
	}
	if trimmed == "t" {
		return "tsp"
	}
	lower := strings.ToLower(trimmed)
	if canonical, ok := unitAliases[lower]; ok {
		return canonical
	}
	switch lower {
	case "tbsp":
		return "Tbsp"
	}
	return lower
}

// volumeInTsp and massInGrams are pure dimensional arithmetic - true for
// every substance, so they need no per-item fact recorded. Anything that
// crosses between the two (a cup of flour weighing 120 g) is a property of
// the specific food and has to come from unit_conversions instead.
var volumeInTsp = map[string]float64{
	"tsp": 1, "Tbsp": 3, "fl oz": 6, "cup": 48, "pint": 96, "quart": 192, "gallon": 768,
	"ml": 0.2028841362, "l": 202.8841362,
}

var massInGrams = map[string]float64{
	"mg": 0.001, "g": 1, "kg": 1000, "oz": 28.349523125, "lb": 453.59237,
}

// builtinFactor returns the multiplier taking a quantity in `from` to one in
// `to`, when both are units of the same physical dimension.
func builtinFactor(from, to string) (float64, bool) {
	if from == to {
		return 1, true
	}
	for _, table := range []map[string]float64{volumeInTsp, massInGrams} {
		f, okFrom := table[from]
		t, okTo := table[to]
		if okFrom && okTo {
			return f / t, true
		}
	}
	return 0, false
}

// ConvertQuantity converts qty from one unit to another for a specific
// pantry item, reporting false (rather than guessing) when there's no basis
// for the conversion.
//
// Three things can bridge the gap, in order of authority: the units being
// the same, dimensional arithmetic (both units measure volume, or both
// measure mass), and a recorded unit_conversions fact - item-specific first,
// then universal. A recorded fact may be combined with arithmetic on either
// side of it, so one "1 cup = 120 g" fact for flour is enough to also answer
// Tbsp -> g and cup -> oz for that item. Two recorded facts are never
// chained: each one is a separate measurement, and composing them compounds
// their error without anyone having decided that's acceptable.
func ConvertQuantity(db *sql.DB, itemID *int64, qty float64, from, to string) (float64, bool, error) {
	from, to = CanonicalUnit(from), CanonicalUnit(to)
	if f, ok := builtinFactor(from, to); ok {
		return qty * f, true, nil
	}

	facts, err := store.ListUnitConversions(db, itemID)
	if err != nil {
		return 0, false, err
	}
	for _, fact := range facts {
		factFrom, factTo := CanonicalUnit(fact.FromUnit), CanonicalUnit(fact.ToUnit)
		if into, ok := builtinFactor(from, factFrom); ok {
			if out, ok := builtinFactor(factTo, to); ok {
				return qty * into * fact.Factor * out, true, nil
			}
		}
		// The same fact read backwards: "1 cup = 120 g" also says
		// "1 g = 1/120 cup".
		if into, ok := builtinFactor(from, factTo); ok {
			if out, ok := builtinFactor(factFrom, to); ok {
				return qty * into / fact.Factor * out, true, nil
			}
		}
	}
	return 0, false, nil
}

// UnitConverterFor returns a converter closure bound to one item, for the
// store-level code that has to compare quantities across units but can't
// import this package.
func UnitConverterFor(db *sql.DB, itemID int64) store.UnitConverter {
	return func(qty float64, from, to string) (float64, bool) {
		converted, ok, err := ConvertQuantity(db, &itemID, qty, from, to)
		if err != nil {
			return 0, false
		}
		return converted, ok
	}
}

// ConversionDisagreementThreshold is how far a newly recorded factor may sit
// from one already derivable for the same item before it's worth flagging.
// Loose enough not to nag about rounding in a reference figure, tight enough
// to catch a genuine contradiction like a 16-slice loaf recorded alongside a
// slice weight that implies 20.
const ConversionDisagreementThreshold = 0.05

// CheckConversionAgreement reports whether a proposed from->to factor
// contradicts what the item's existing facts already imply, returning the
// implied factor when it does.
//
// Facts are consulted one at a time and the first usable one wins, so two
// mutually inconsistent facts about the same item don't error - they just
// make the answer depend on which unit the caller happened to ask in. That
// is precisely the kind of quiet disagreement this project tries not to
// have, and the moment to catch it is when the second fact is written.
func CheckConversionAgreement(db *sql.DB, itemID *int64, from, to string, factor float64) (implied float64, disagrees bool, err error) {
	facts, err := store.ListUnitConversions(db, itemID)
	if err != nil {
		return 0, false, err
	}
	// Composing two facts is refused when *answering* a conversion, because
	// a silently compounded error is worse than an honest gap. Composing
	// them to check a third against is a different matter: nothing is
	// derived from it, and it's the only way to notice that "1 slice =
	// 0.05 loaf" and a slice weight implying 20 slices can't both be true.
	existing, ok := derivedFactor(facts, CanonicalUnit(from), CanonicalUnit(to), 2)
	if !ok || existing == 0 {
		return 0, false, nil
	}
	ratio := factor / existing
	if ratio > 1+ConversionDisagreementThreshold || ratio < 1-ConversionDisagreementThreshold {
		return existing, true, nil
	}
	return existing, false, nil
}

// derivedFactor finds what the existing facts already imply for from->to,
// using at most maxFacts recorded facts with built-in arithmetic free at
// every step. Breadth-first, so the answer uses as few recorded facts as
// possible. Validation only - see CheckConversionAgreement.
func derivedFactor(facts []store.UnitConversion, from, to string, maxFacts int) (float64, bool) {
	type node struct {
		unit   string
		factor float64
		hops   int
	}
	queue := []node{{unit: from, factor: 1}}
	seen := map[string]bool{from: true}

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if f, ok := builtinFactor(n.unit, to); ok {
			return n.factor * f, true
		}
		if n.hops >= maxFacts {
			continue
		}
		for _, fact := range facts {
			factFrom, factTo := CanonicalUnit(fact.FromUnit), CanonicalUnit(fact.ToUnit)
			if fact.Factor == 0 {
				continue
			}
			if b, ok := builtinFactor(n.unit, factFrom); ok && !seen[factTo] {
				seen[factTo] = true
				queue = append(queue, node{factTo, n.factor * b * fact.Factor, n.hops + 1})
			}
			if b, ok := builtinFactor(n.unit, factTo); ok && !seen[factFrom] {
				seen[factFrom] = true
				queue = append(queue, node{factFrom, n.factor * b / fact.Factor, n.hops + 1})
			}
		}
	}
	return 0, false
}

// packageSizePattern finds a quantity-and-unit stated inside receipt text,
// e.g. the "40 oz" in "Great Value Creamy Peanut Butter, 40 oz".
var packageSizePattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*-?\s*(fl oz|oz|lb|lbs|kg|g|ml|l|ct|count|pack)\b`)

// StatedPackageSize reports the largest quantity the text states in the same
// unit the caller is recording the line in, so a mismatch between the two can
// be flagged.
//
// This exists because of a repeated, quiet, expensive mistake: a line like
// "Chicken Nuggets, 32 oz" logged as quantity 1 with unit oz puts the whole
// package's price on a single ounce, and nothing downstream can tell that
// from a genuine $5.97-per-ounce product. It happened three times before
// anyone noticed. The largest match wins because product names often carry
// smaller incidental numbers ("3-Pack, 0.15 oz Each").
func StatedPackageSize(rawText, unit string) (float64, bool) {
	want := CanonicalUnit(unit)
	var best float64
	for _, m := range packageSizePattern.FindAllStringSubmatch(rawText, -1) {
		if CanonicalUnit(m[2]) != want {
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if v > best {
			best = v
		}
	}
	return best, best > 0
}
