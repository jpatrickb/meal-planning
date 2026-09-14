package store

import (
	"math"
	"testing"
)

// gramsPerOunce lets the test converter stand in for a recorded oz <-> g
// conversion without pulling in the domain package (which imports this one).
const gramsPerOunce = 28.349523125

func ozGramConverter(qty float64, from, to string) (float64, bool) {
	switch {
	case from == to:
		return qty, true
	case from == "oz" && to == "g":
		return qty * gramsPerOunce, true
	case from == "g" && to == "oz":
		return qty / gramsPerOunce, true
	}
	return 0, false
}

func TestDecrementFIFOReachesStockInAnotherUnitWhenConvertible(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Chicken Breast", "protein_meat", "lb")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (item_id, quantity, unit, unit_cost, acquired_date, status)
		VALUES (?, 500, 'g', 0.01, '2026-01-01', 'on_hand')`, itemID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// 10 oz asked for, only grams on hand: reachable via the converter.
	lots, err := DecrementPantryStockFIFODetailed(tx, itemID, "oz", 10, "consumed", ozGramConverter)
	if err != nil {
		t.Fatalf("decrementing: %v", err)
	}
	if len(lots) != 1 {
		t.Fatalf("consumed %d lots, want 1", len(lots))
	}
	// The lot must be debited in its own unit: 10 oz is 283.5 g.
	if want := 10 * gramsPerOunce; math.Abs(lots[0].Quantity-want) > 1e-6 {
		t.Errorf("debited %v, want %v (in the lot's own unit)", lots[0].Quantity, want)
	}
	if lots[0].Unit != "g" {
		t.Errorf("reported unit %q, want the lot's own unit g", lots[0].Unit)
	}
	if math.Abs(lots[0].QuantityAsked-10) > 1e-6 {
		t.Errorf("QuantityAsked = %v, want 10 (the unit the caller asked in)", lots[0].QuantityAsked)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var remaining float64
	if err := db.QueryRow(`SELECT quantity FROM pantry_stock WHERE item_id = ?`, itemID).Scan(&remaining); err != nil {
		t.Fatalf("reading remaining: %v", err)
	}
	if want := 500 - 10*gramsPerOunce; math.Abs(remaining-want) > 1e-6 {
		t.Errorf("remaining %v g, want %v g", remaining, want)
	}
}

// Without a conversion the old behavior has to stand: stock in another unit
// is not silently summed in, and the decrement refuses rather than guessing.
func TestDecrementFIFOIgnoresUnconvertibleUnits(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Onions", "veg_fresh_frozen", "count")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (item_id, quantity, unit, unit_cost, acquired_date, status)
		VALUES (?, 3, 'lb', 1.00, '2026-01-01', 'on_hand')`, itemID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := DecrementPantryStockFIFODetailed(tx, itemID, "count", 2, "consumed", ozGramConverter); err == nil {
		t.Fatal("expected insufficient-stock error, got nil")
	}
}

func TestGetItemUnitCostConvertsFromThePricedUnit(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Chicken Breast", "protein_meat", "lb")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (item_id, quantity, unit, unit_cost, acquired_date, status)
		VALUES (?, 500, 'g', 0.01, '2026-01-01', 'on_hand')`, itemID)

	cost, ok, err := GetItemUnitCost(db, itemID, "oz", ozGramConverter)
	if err != nil || !ok {
		t.Fatalf("GetItemUnitCost: ok = %v, err = %v", ok, err)
	}
	if want := 0.01 * gramsPerOunce; math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost per oz = %v, want %v", cost, want)
	}
}
