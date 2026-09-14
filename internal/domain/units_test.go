package domain

import (
	"math"
	"testing"

	"github.com/jpatrickb/meal-planning/internal/store"
)

func TestCanonicalUnit(t *testing.T) {
	cases := map[string]string{
		"c": "cup", "cups": "cup", "Cup": "cup", " cup ": "cup",
		"T": "Tbsp", "tbsp": "Tbsp", "Tbsp": "Tbsp", "tablespoons": "Tbsp",
		"t": "tsp", "tsp": "tsp", "teaspoon": "tsp",
		"lbs": "lb", "Pounds": "lb", "ounces": "oz",
		"cloves": "clove", "small count": "count", "loaves": "loaf",
		"bag": "bag", "": "",
	}
	for in, want := range cases {
		if got := CanonicalUnit(in); got != want {
			t.Errorf("CanonicalUnit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConvertQuantity(t *testing.T) {
	db := newTestDB(t)
	itemID := mustCreateItem(t, db, "All-Purpose Flour", "baking", "cup")
	// One recorded density fact for this item, the way `meal pantry
	// set-conversion` would write it.
	if _, err := store.CreateUnitConversion(db, &itemID, "cup", "g", 120); err != nil {
		t.Fatalf("recording conversion: %v", err)
	}

	cases := []struct {
		name     string
		qty      float64
		from, to string
		want     float64
		wantOK   bool
	}{
		{"identity", 3, "cup", "cup", 3, true},
		{"alias counts as identity", 3, "c", "cup", 3, true},
		{"volume arithmetic needs no fact", 2, "cup", "Tbsp", 32, true},
		{"mass arithmetic needs no fact", 1, "lb", "oz", 16, true},
		{"recorded fact", 2, "cup", "g", 240, true},
		{"recorded fact, read backwards", 240, "g", "cup", 2, true},
		{"arithmetic feeding into a fact", 16, "Tbsp", "g", 120, true},
		{"arithmetic on both sides of a fact", 16, "Tbsp", "oz", 120 / 28.349523125, true},
		{"no basis to cross dimensions", 1, "cup", "count", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ConvertQuantity(db, &itemID, tc.qty, tc.from, tc.to)
			if err != nil {
				t.Fatalf("ConvertQuantity: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A fact recorded for one item must not leak onto another - a cup of flour
// weighing 120 g says nothing about a cup of sugar.
func TestConvertQuantityDoesNotShareItemFacts(t *testing.T) {
	db := newTestDB(t)
	flour := mustCreateItem(t, db, "All-Purpose Flour", "baking", "cup")
	sugar := mustCreateItem(t, db, "Granulated Sugar", "baking", "cup")
	if _, err := store.CreateUnitConversion(db, &flour, "cup", "g", 120); err != nil {
		t.Fatalf("recording conversion: %v", err)
	}
	if _, ok, err := ConvertQuantity(db, &sugar, 1, "cup", "g"); err != nil || ok {
		t.Fatalf("cup->g for sugar: ok = %v (err %v), want false", ok, err)
	}
}

func TestConvertQuantityUsesUniversalFact(t *testing.T) {
	db := newTestDB(t)
	itemID := mustCreateItem(t, db, "Eggs", "protein_other", "count")
	if _, err := store.CreateUnitConversion(db, nil, "dozen", "count", 12); err != nil {
		t.Fatalf("recording universal conversion: %v", err)
	}
	got, ok, err := ConvertQuantity(db, &itemID, 2, "dozen", "count")
	if err != nil || !ok {
		t.Fatalf("dozen->count: ok = %v, err = %v", ok, err)
	}
	if got != 24 {
		t.Errorf("got %v, want 24", got)
	}
}

// The case this check exists for: a slice weight and a loaf weight that
// together imply 16 slices, against a slice->loaf factor saying 20.
// Neither fact is wrong on its own, and answering a conversion never
// composes them, so nothing else would ever notice they disagree.
func TestCheckConversionAgreementCatchesACrossFactContradiction(t *testing.T) {
	db := newTestDB(t)
	bread := mustCreateItem(t, db, "Sprouted Wheat Bread", "grains", "loaf")
	for _, f := range []struct {
		from, to string
		factor   float64
	}{{"loaf", "g", 680}, {"slice", "g", 42.5}} {
		if _, err := store.CreateUnitConversion(db, &bread, f.from, f.to, f.factor); err != nil {
			t.Fatalf("recording %s->%s: %v", f.from, f.to, err)
		}
	}

	implied, disagrees, err := CheckConversionAgreement(db, &bread, "slice", "loaf", 0.05)
	if err != nil {
		t.Fatalf("CheckConversionAgreement: %v", err)
	}
	if !disagrees {
		t.Fatal("a 20-slice loaf against a 16-slice one was not flagged")
	}
	if math.Abs(implied-0.0625) > 1e-9 {
		t.Errorf("implied = %v, want 0.0625 (42.5 g / 680 g)", implied)
	}

	// The consistent value must not be flagged.
	if _, disagrees, err := CheckConversionAgreement(db, &bread, "slice", "loaf", 0.0625); err != nil || disagrees {
		t.Errorf("consistent factor flagged: disagrees = %v, err = %v", disagrees, err)
	}
}

// Nothing to compare against is not a disagreement.
func TestCheckConversionAgreementIsSilentWithNoBasis(t *testing.T) {
	db := newTestDB(t)
	item := mustCreateItem(t, db, "Mystery Powder", "misc", "scoop")
	implied, disagrees, err := CheckConversionAgreement(db, &item, "scoop", "g", 30)
	if err != nil || disagrees || implied != 0 {
		t.Errorf("got implied=%v disagrees=%v err=%v, want 0/false/nil", implied, disagrees, err)
	}
}

// Answering a conversion still refuses to chain two facts, even though the
// consistency check composes them. The two behaviors are deliberately
// different and it would be easy to accidentally unify them.
func TestConvertQuantityStillRefusesToChainTwoFacts(t *testing.T) {
	db := newTestDB(t)
	bread := mustCreateItem(t, db, "Sprouted Wheat Bread", "grains", "loaf")
	if _, err := store.CreateUnitConversion(db, &bread, "loaf", "g", 680); err != nil {
		t.Fatalf("recording loaf->g: %v", err)
	}
	if _, err := store.CreateUnitConversion(db, &bread, "slice", "g", 42.5); err != nil {
		t.Fatalf("recording slice->g: %v", err)
	}
	if _, ok, err := ConvertQuantity(db, &bread, 1, "slice", "loaf"); err != nil || ok {
		t.Errorf("slice->loaf resolved by chaining two facts (ok=%v, err=%v); it needs its own recorded fact", ok, err)
	}
}

// The three real receipt lines that motivated the warning, plus the ones it
// must stay quiet about.
func TestStatedPackageSize(t *testing.T) {
	cases := []struct {
		raw, unit string
		want      float64
		wantOK    bool
	}{
		{"Great Value Creamy Peanut Butter, 40 oz", "oz", 40, true},
		{"Great Value Fully Cooked Chicken Nuggets, 32 oz (Frozen)", "oz", 32, true},
		{"Great Value 4% Milkfat Minimum Small Curd Cottage Cheese, 24 oz", "oz", 24, true},
		// The unit recorded is a package unit, so an ounce figure in the
		// text says nothing about the quantity being logged.
		{"Malt-O-Meal Frosted Mini Spooners, 60 oz Resealable Bag", "bag", 0, false},
		// Incidental smaller numbers must not win over the real size.
		{"ChapStick Variety Pack, 3-Pack, 0.15 oz Each", "oz", 0.15, true},
		{"Farmland Diced Ham 16oz", "oz", 16, true},
		{"GV 28OZ TOM", "can", 0, false},
		{"Fresh Whole Green Onion 1 Bunch", "oz", 0, false},
	}
	for _, tc := range cases {
		got, ok := StatedPackageSize(tc.raw, tc.unit)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("StatedPackageSize(%q, %q) = %v, %v; want %v, %v", tc.raw, tc.unit, got, ok, tc.want, tc.wantOK)
		}
	}
}
