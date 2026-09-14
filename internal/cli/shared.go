package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

// parseItemQtyOverrides parses "5=0.5,12=2" into a map of pantry item id ->
// override quantity, for meal cook's --ingredient-qty flag.
func parseItemQtyOverrides(s string) (map[int64]float64, error) {
	if s == "" {
		return nil, nil
	}
	out := map[int64]float64{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, qtyStr, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid entry %q: expected item-id=qty", part)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid item id in %q: %w", part, err)
		}
		qty, err := strconv.ParseFloat(strings.TrimSpace(qtyStr), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid quantity in %q: %w", part, err)
		}
		if qty < 0 {
			return nil, fmt.Errorf("quantity in %q must not be negative", part)
		}
		out[id] = qty
	}
	return out, nil
}

// parseItemIDSet parses "5,12" into a set of pantry item ids, for meal
// cook's --accept-zero-stock flag.
func parseItemIDSet(s string) (map[int64]bool, error) {
	if s == "" {
		return nil, nil
	}
	out := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid item id %q: %w", part, err)
		}
		out[id] = true
	}
	return out, nil
}

// pantryDepletion is one "item-id=qty[:unit]" entry from a --deplete-pantry
// flag, kept as an ordered slice (not a map) so decrements happen, and any
// resulting error reports, in a deterministic order.
type pantryDepletion struct {
	ItemID int64
	Qty    float64
	Unit   string // "" means: use the item's default_unit
}

// parsePantryDepletions parses "12=0.5,7=200:g" into ordered pantry
// depletions, for meal log's --deplete-pantry flag. A ":unit" suffix is
// optional; omitting it defers to the item's own default_unit once looked up.
func parsePantryDepletions(s string) ([]pantryDepletion, error) {
	if s == "" {
		return nil, nil
	}
	var out []pantryDepletion
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, rest, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid entry %q: expected item-id=qty or item-id=qty:unit", part)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid item id in %q: %w", part, err)
		}
		qtyStr, unit, _ := strings.Cut(rest, ":")
		qty, err := strconv.ParseFloat(strings.TrimSpace(qtyStr), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid quantity in %q: %w", part, err)
		}
		if qty <= 0 {
			return nil, fmt.Errorf("quantity in %q must be positive", part)
		}
		out = append(out, pantryDepletion{ItemID: id, Qty: qty, Unit: strings.TrimSpace(unit)})
	}
	return out, nil
}

// decrementPantryFIFOOrHint wraps store.DecrementPantryStockFIFODetailed (or,
// when acceptZeroStock is true, DecrementPantryStockFIFOPartial) with the
// friendly ErrInsufficientStock hint shared by `meal cook` and `meal log
// --deplete-pantry`: pointing at --accept-zero-stock, and, when stock exists
// in a unit no recorded conversion can reach from this one, spelling out the
// set-conversion call that would bridge it. Stock in another unit is already
// used automatically when a conversion exists, so reaching this message
// means the fact itself is what's missing.
func decrementPantryFIFOOrHint(db *sql.DB, tx *sql.Tx, itemID int64, unit string, qty float64, acceptZeroStock bool) ([]store.ConsumedLot, error) {
	if acceptZeroStock {
		return store.DecrementPantryStockFIFOPartial(tx, itemID, unit, qty, "consumed", domain.UnitConverterFor(db, itemID))
	}
	lots, err := store.DecrementPantryStockFIFODetailed(tx, itemID, unit, qty, "consumed", domain.UnitConverterFor(db, itemID))
	if err != nil {
		if errors.Is(err, store.ErrInsufficientStock) {
			hint := fmt.Sprintf("re-run with --accept-zero-stock %d to proceed with what's on hand, or fix the pantry record first", itemID)
			if other, oerr := store.OtherUnitStock(db, itemID, unit); oerr == nil && len(other) > 0 {
				parts := make([]string, len(other))
				for i, o := range other {
					parts[i] = fmt.Sprintf("%.2f %s", o.Quantity, o.Unit)
				}
				hint = fmt.Sprintf("stock exists, but in a unit no recorded conversion reaches from %s (%s) - record one with `meal pantry set-conversion --item-id %d --from %s --to %s --factor N`, or %s",
					unit, strings.Join(parts, ", "), itemID, unit, other[0].Unit, hint)
			}
			return nil, fmt.Errorf("%w for item %d (%s)", err, itemID, hint)
		}
		return nil, fmt.Errorf("decrementing pantry for item %d: %w", itemID, err)
	}
	return lots, nil
}

var validPersons = map[string]bool{"patrick": true, "thea": true}
var validMealSlots = map[string]bool{"breakfast": true, "lunch": true, "dinner": true, "snack": true}

// validPantryCategories mirrors the pantry_categories seed data in
// 0001_init.sql. Kept in sync manually since it's a fixed, deliberately
// small taxonomy - not worth a DB round trip to validate against.
var validPantryCategories = map[string]bool{
	"grains": true, "protein_meat": true, "protein_legume": true, "protein_other": true,
	"veg_fresh_frozen": true, "veg_canned": true, "fruit": true, "baking": true,
	"oils_condiments": true, "sauces": true, "dairy": true, "misc": true,
}

func validateCategory(s string) error {
	if !validPantryCategories[s] {
		return fmt.Errorf("invalid --category %q: must be one of grains, protein_meat, protein_legume, protein_other, veg_fresh_frozen, veg_canned, fruit, baking, oils_condiments, sauces, dairy, misc", s)
	}
	return nil
}

func validatePerson(s string) error {
	if !validPersons[s] {
		return fmt.Errorf("invalid --person %q: must be patrick or thea", s)
	}
	return nil
}

// validateMealSlot returns nil (no error, nil pointer) for an empty string -
// meal_slot is optional. A non-empty value must be one of the four slots.
func validateMealSlot(s string) (*string, error) {
	if s == "" {
		return nil, nil
	}
	if !validMealSlots[s] {
		return nil, fmt.Errorf("invalid --meal-slot %q: must be breakfast, lunch, dinner, or snack", s)
	}
	return &s, nil
}

func todayDate() string {
	return time.Now().Format("2006-01-02")
}

// warnIfNoMacros prints a stderr warning when a recipe has no local macros
// set, mirroring the existing cost warning in `meal cook`. Without it,
// consumption rows for that recipe silently have blank macros and macro
// analytics quietly under-count that day - this makes the gap visible
// instead of invisible.
func warnIfNoMacros(recipeSlug string, res domain.RecipeMacroResolution) {
	if res.Source != "unknown" {
		return
	}
	if len(res.UnknownItems) > 0 {
		fmt.Fprintf(os.Stderr, "warning: macros unknown for %s (%s), macro totals will be incomplete for this entry - fill the gap with `meal pantry set-conversion` / `meal pantry link-nutrition`, or set them directly with `meal recipe set-meta %s --calories X --protein X --carbs X --fat X --fiber X`\n",
			recipeSlug, strings.Join(res.UnknownItems, "; "), recipeSlug)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: no macros set for %s, macro totals will be incomplete for this entry (set with `meal recipe set-meta %s --calories X --protein X --carbs X --fat X --fiber X`)\n", recipeSlug, recipeSlug)
}

// resolveCookEventMacros prefers what a cook actually consumed, divided by
// the servings it actually yielded, over the recipe in the abstract - the
// two disagree whenever a batch was scaled, had an ingredient overridden, or
// yielded a different number of servings than the recipe claims. The recipe
// resolution is the fallback for an event with nothing recorded against it
// (a cook logged before ingredient mapping existed), and a manual
// recipe_meta value still wins outright.
func resolveCookEventMacros(db *sql.DB, ce store.CookEvent, meta store.RecipeMeta) (domain.RecipeMacroResolution, error) {
	if meta.MacrosPerServing != nil {
		return domain.RecipeMacroResolution{PerServing: *meta.MacrosPerServing, Source: "manual"}, nil
	}
	perServing, source, unknown, err := domain.ComputeCookEventMacros(db, ce.ID)
	if err != nil {
		return domain.RecipeMacroResolution{}, fmt.Errorf("computing macros for cook event %d: %w", ce.ID, err)
	}
	if len(unknown) == 0 {
		res := domain.RecipeMacroResolution{PerServing: perServing, Source: "computed"}
		if source == "estimated" {
			res.Source = "computed_estimated"
		}
		return res, nil
	}
	res, err := domain.ResolveRecipeMacros(db, ce.RecipeID, meta, ce.ServingsYielded)
	if err != nil {
		return domain.RecipeMacroResolution{}, fmt.Errorf("resolving macros: %w", err)
	}
	if res.Source == "unknown" && res.Reason == "" {
		res.Reason = strings.Join(unknown, "; ")
	}
	return res, nil
}

// applyRecipeMacros scales a resolved per-serving figure onto one
// consumption entry. macro_source records where it came from: "local_meta"
// for a hand-set recipe_meta value, "usda" for one computed from the
// ingredients' own nutrition data.
func applyRecipeMacros(entry *store.ConsumptionEntry, res domain.RecipeMacroResolution, servings float64) {
	if res.Source == "unknown" {
		return
	}
	m := res.PerServing
	cal, pro, carb, fat, fib := m.Calories*servings, m.Protein*servings, m.Carbs*servings, m.Fat*servings, m.Fiber*servings
	src := "local_meta"
	switch res.Source {
	case "computed":
		src = "usda"
	case "computed_estimated":
		src = "estimated"
	}
	entry.Calories, entry.Protein, entry.Carbs, entry.Fat, entry.Fiber = &cal, &pro, &carb, &fat, &fib
	entry.MacroSource = &src
}

// periodSince converts a --period value (week/month/year/all) into a cutoff
// date string for a "consumed_date >= cutoff" filter. "" (no cutoff) for "all".
func periodSince(period string) (string, error) {
	switch period {
	case "week":
		return time.Now().AddDate(0, 0, -7).Format("2006-01-02"), nil
	case "month":
		return time.Now().AddDate(0, -1, 0).Format("2006-01-02"), nil
	case "year":
		return time.Now().AddDate(-1, 0, 0).Format("2006-01-02"), nil
	case "all":
		return "", nil
	default:
		return "", fmt.Errorf("invalid --period %q: must be week, month, year, or all", period)
	}
}

// personAmount is one "person=servings" pair from a --eaten-now flag, kept
// as an ordered slice (not a map) so consumption rows are inserted in a
// deterministic order.
type personAmount struct {
	Person   string
	Servings float64
}

// parsePersonAmounts parses "patrick=1,thea=0.5" into ordered pairs,
// validating each person name and that amounts are positive.
func parsePersonAmounts(s string) ([]personAmount, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]personAmount, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		person, amtStr, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid entry %q: expected person=servings", part)
		}
		person = strings.TrimSpace(person)
		if err := validatePerson(person); err != nil {
			return nil, err
		}
		amt, err := strconv.ParseFloat(strings.TrimSpace(amtStr), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid servings amount in %q: %w", part, err)
		}
		if amt <= 0 {
			return nil, fmt.Errorf("servings amount in %q must be positive", part)
		}
		out = append(out, personAmount{Person: person, Servings: amt})
	}
	return out, nil
}

// renderResult emits payload as JSON under --json, or the formatted human
// line otherwise.
//
// For commands whose success output is a single confirmation sentence: the
// sentence is what a person wants, but a script needs the ids inside it -
// `shop start` in particular hands back the purchase id every subsequent
// add-item call needs. --json is documented as working everywhere, so it has
// to actually work everywhere.
func renderResult(payload any, format string, args ...any) error {
	if jsonOutput {
		return output.Render(os.Stdout, true, payload, nil)
	}
	fmt.Printf(format, args...)
	return nil
}
