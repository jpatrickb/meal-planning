package domain

import (
	"database/sql"
	"math"
	"testing"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// seedItemWithMacros creates a pantry item, gives it label nutrition, and
// records a cup -> g anchor, which is the minimum an ingredient needs to
// contribute to a recipe's computed macros.
func seedItemWithMacros(t *testing.T, db *sql.DB, name string, calPer100g, proPer100g, gramsPerCup float64) int64 {
	t.Helper()
	id := mustCreateItem(t, db, name, "misc", "cup")
	if err := store.UpsertItemNutrition(db, store.ItemNutrition{
		ItemID: id, CaloriesPer100g: calPer100g, ProteinPer100g: proPer100g, Source: "label",
	}); err != nil {
		t.Fatalf("setting nutrition for %s: %v", name, err)
	}
	if _, err := store.CreateUnitConversion(db, &id, "cup", "g", gramsPerCup); err != nil {
		t.Fatalf("setting conversion for %s: %v", name, err)
	}
	return id
}

func TestDepletedItemMacrosScalesByGrams(t *testing.T) {
	db := newTestDB(t)
	// 200 cal and 10 g protein per 100 g; one cup weighs 240 g.
	id := seedItemWithMacros(t, db, "Test Sauce", 200, 10, 240)

	macros, source, unknown, err := DepletedItemMacros(db, map[int64]struct {
		Qty  float64
		Unit string
	}{id: {Qty: 0.5, Unit: "cup"}})
	if err != nil {
		t.Fatalf("DepletedItemMacros: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unexpected gaps: %v", unknown)
	}
	if source != "usda" {
		t.Errorf("source = %q, want usda", source)
	}
	// Half a cup is 120 g, so 1.2 x the per-100g figures.
	if math.Abs(macros.Calories-240) > 1e-6 || math.Abs(macros.Protein-12) > 1e-6 {
		t.Errorf("got %.2f cal / %.2f g protein, want 240 / 12", macros.Calories, macros.Protein)
	}
}

// An entry may not claim more authority than its weakest input: one
// estimated item makes the whole entry's macros estimated.
func TestDepletedItemMacrosDowngradesSourceForEstimates(t *testing.T) {
	db := newTestDB(t)
	solid := seedItemWithMacros(t, db, "Known Item", 100, 5, 100)
	guessed := seedItemWithMacros(t, db, "Guessed Blend", 300, 5, 100)
	if err := store.UpsertItemNutrition(db, store.ItemNutrition{
		ItemID: guessed, CaloriesPer100g: 300, ProteinPer100g: 5, Source: "estimated",
	}); err != nil {
		t.Fatalf("marking estimated: %v", err)
	}

	_, source, _, err := DepletedItemMacros(db, map[int64]struct {
		Qty  float64
		Unit string
	}{solid: {Qty: 1, Unit: "cup"}, guessed: {Qty: 1, Unit: "cup"}})
	if err != nil {
		t.Fatalf("DepletedItemMacros: %v", err)
	}
	if source != "estimated" {
		t.Errorf("source = %q, want estimated", source)
	}
}

// A missing conversion has to be reported, not silently dropped - an
// ingredient contributing zero would understate the total while still
// looking like a complete answer.
func TestDepletedItemMacrosReportsMissingConversion(t *testing.T) {
	db := newTestDB(t)
	id := mustCreateItem(t, db, "Mystery Item", "misc", "count")
	if err := store.UpsertItemNutrition(db, store.ItemNutrition{
		ItemID: id, CaloriesPer100g: 100, Source: "label",
	}); err != nil {
		t.Fatalf("setting nutrition: %v", err)
	}

	_, _, unknown, err := DepletedItemMacros(db, map[int64]struct {
		Qty  float64
		Unit string
	}{id: {Qty: 2, Unit: "count"}})
	if err != nil {
		t.Fatalf("DepletedItemMacros: %v", err)
	}
	if len(unknown) != 1 {
		t.Fatalf("gaps = %v, want one entry naming the missing conversion", unknown)
	}
}

// Salt has real, honest zeroes; a food USDA returned with no macro rows at
// all also stores as zeroes. Only the first should contribute.
func TestGetItemMacrosDistinguishesHonestZeroFromMissingData(t *testing.T) {
	db := newTestDB(t)
	salt := mustCreateItem(t, db, "Salt", "misc", "tsp")
	empty := mustCreateItem(t, db, "Mystery Oil", "misc", "Tbsp")

	mustCache(t, db, 1, "Salt, table", `{"fdcId":1,"description":"Salt, table","foodNutrients":[
		{"nutrientId":1008,"nutrientName":"Energy","unitName":"KCAL","value":0},
		{"nutrientId":1003,"nutrientName":"Protein","unitName":"G","value":0}]}`)
	mustCache(t, db, 2, "Mystery Oil", `{"fdcId":2,"description":"Mystery Oil","foodNutrients":[
		{"nutrientId":1257,"nutrientName":"Fatty acids, total trans","unitName":"G","value":0.1}]}`)
	if err := store.SetPantryItemFDCID(db, salt, 1); err != nil {
		t.Fatalf("linking salt: %v", err)
	}
	if err := store.SetPantryItemFDCID(db, empty, 2); err != nil {
		t.Fatalf("linking empty: %v", err)
	}

	if _, ok, err := GetItemMacros(db, salt); err != nil || !ok {
		t.Errorf("salt: ok = %v (err %v), want true - zero is its real value", ok, err)
	}
	if _, ok, err := GetItemMacros(db, empty); err != nil || ok {
		t.Errorf("no-macro-data food: ok = %v (err %v), want false", ok, err)
	}
}

func mustCache(t *testing.T, db *sql.DB, fdcID int, description, rawJSON string) {
	t.Helper()
	if err := store.UpsertNutritionCache(db, store.NutritionCacheEntry{
		FDCID: fdcID, Description: description, RawJSON: rawJSON, FetchedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("caching %s: %v", description, err)
	}
}
