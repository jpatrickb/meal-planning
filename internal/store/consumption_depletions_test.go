package store

import (
	"math"
	"testing"
)

// Voiding a log entry has to be a true undo: the stock goes back onto the
// lot it came from, keeping that lot's acquired date, expiration and real
// purchase price.
func TestVoidLogEntryRestoresStockToTheOriginalLot(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Bananas", "fruit", "count")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, unit_cost, acquired_date, expires_date, status)
		VALUES (77, ?, 3, 'count', 0.25, '2026-01-01', '2026-01-08', 'on_hand')`, itemID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	food := "Banana"
	logID, err := InsertConsumptionEntry(tx, ConsumptionEntry{
		ConsumedDate: "2026-01-02", Person: "patrick", Source: "from_pantry",
		FreeTextFood: &food, Servings: 1, Cost: 0.25,
	})
	if err != nil {
		t.Fatalf("inserting entry: %v", err)
	}
	if _, err := DecrementPantryStockFIFODetailed(tx, itemID, "count", 1, "consumed", nil); err != nil {
		t.Fatalf("decrementing: %v", err)
	}
	if _, err := InsertConsumptionDepletion(tx, ConsumptionDepletion{
		ConsumptionLogID: logID, ItemID: itemID, PantryStockID: 77, Quantity: 1, Unit: "count",
	}); err != nil {
		t.Fatalf("recording depletion: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	v, err := VoidLogEntry(db, logID)
	if err != nil {
		t.Fatalf("voiding: %v", err)
	}
	if len(v.RestoredLots) != 1 || v.RestoredLots[0].StockID != 77 {
		t.Fatalf("restored %+v, want one lot 77", v.RestoredLots)
	}

	var qty, cost float64
	var acquired, expires, status string
	if err := db.QueryRow(`SELECT quantity, unit_cost, acquired_date, expires_date, status FROM pantry_stock WHERE id = 77`).
		Scan(&qty, &cost, &acquired, &expires, &status); err != nil {
		t.Fatalf("reading lot: %v", err)
	}
	if math.Abs(qty-3) > 1e-9 {
		t.Errorf("quantity = %v, want the original 3", qty)
	}
	if cost != 0.25 || acquired != "2026-01-01" || expires != "2026-01-08" || status != "on_hand" {
		t.Errorf("lot identity changed: cost %v acquired %q expires %q status %q", cost, acquired, expires, status)
	}

	// The depletion rows describe an entry that no longer exists.
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM consumption_depletions WHERE consumption_log_id = ?`, logID).Scan(&remaining); err != nil {
		t.Fatalf("counting depletions: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d depletion rows left behind, want 0", remaining)
	}
}

// A fully-consumed lot has to come back on hand, not stay marked consumed
// with a nonzero quantity.
func TestVoidLogEntryReopensAFullyConsumedLot(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Milk", "dairy", "cup")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, unit_cost, acquired_date, status)
		VALUES (88, ?, 2, 'cup', 0.20, '2026-01-01', 'on_hand')`, itemID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	food := "Glass of milk"
	logID, err := InsertConsumptionEntry(tx, ConsumptionEntry{
		ConsumedDate: "2026-01-02", Person: "thea", Source: "from_pantry",
		FreeTextFood: &food, Servings: 1,
	})
	if err != nil {
		t.Fatalf("inserting entry: %v", err)
	}
	if _, err := DecrementPantryStockFIFODetailed(tx, itemID, "cup", 2, "consumed", nil); err != nil {
		t.Fatalf("decrementing: %v", err)
	}
	if _, err := InsertConsumptionDepletion(tx, ConsumptionDepletion{
		ConsumptionLogID: logID, ItemID: itemID, PantryStockID: 88, Quantity: 2, Unit: "cup",
	}); err != nil {
		t.Fatalf("recording depletion: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, err := VoidLogEntry(db, logID); err != nil {
		t.Fatalf("voiding: %v", err)
	}
	var qty float64
	var status string
	if err := db.QueryRow(`SELECT quantity, status FROM pantry_stock WHERE id = 88`).Scan(&qty, &status); err != nil {
		t.Fatalf("reading lot: %v", err)
	}
	if math.Abs(qty-2) > 1e-9 || status != "on_hand" {
		t.Errorf("lot is %v %s, want 2 on_hand", qty, status)
	}
}

// Voiding a leftover draw has to give the servings back to the cook event.
// Without it the ledger silently loses them: the consumption row is gone,
// but servings_remaining still reflects a helping nobody ate - so the
// leftovers list under-reports what's actually in the fridge, and re-logging
// the entry correctly fails with "not enough servings remaining".
func TestVoidLogEntryReturnsServingsToTheCookEvent(t *testing.T) {
	db := newTestDB(t)
	mustExec(t, db, `INSERT INTO recipes (id, title, category, raw_json, synced_at) VALUES ('r1','R1','mains','{}','2026-08-22')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	cookID, err := InsertCookEvent(tx, CookEvent{
		RecipeID: "r1", CookedDate: "2026-08-23", ServingsYielded: 10, TotalCost: 5.19,
	})
	if err != nil {
		t.Fatalf("inserting cook event: %v", err)
	}
	if err := DecrementCookEventServings(tx, cookID, 2); err != nil {
		t.Fatalf("decrementing servings: %v", err)
	}
	food := "R1 leftovers"
	logID, err := InsertConsumptionEntry(tx, ConsumptionEntry{
		ConsumedDate: "2026-08-28", Person: "patrick", Source: "leftover",
		CookEventID: &cookID, FreeTextFood: &food, Servings: 2, Cost: 0,
	})
	if err != nil {
		t.Fatalf("inserting entry: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	v, err := VoidLogEntry(db, logID)
	if err != nil {
		t.Fatalf("voiding: %v", err)
	}
	if v.CookEventID == nil || *v.CookEventID != cookID {
		t.Errorf("CookEventID = %v, want %d", v.CookEventID, cookID)
	}
	if math.Abs(v.ServingsReturned-2) > 1e-9 {
		t.Errorf("ServingsReturned = %v, want 2", v.ServingsReturned)
	}

	var remaining float64
	if err := db.QueryRow(`SELECT servings_remaining FROM cook_events WHERE id = ?`, cookID).Scan(&remaining); err != nil {
		t.Fatalf("reading cook event: %v", err)
	}
	if math.Abs(remaining-10) > 1e-9 {
		t.Errorf("servings_remaining = %v, want the full 10 back", remaining)
	}
}

// An ad-hoc entry has no cook event, so a void must not invent servings for one.
func TestVoidLogEntryReturnsNoServingsForANonLeftoverEntry(t *testing.T) {
	db := newTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	food := "Protein bar"
	logID, err := InsertConsumptionEntry(tx, ConsumptionEntry{
		ConsumedDate: "2026-08-28", Person: "thea", Source: "purchased_ready",
		FreeTextFood: &food, Servings: 1, Cost: 1.50,
	})
	if err != nil {
		t.Fatalf("inserting entry: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	v, err := VoidLogEntry(db, logID)
	if err != nil {
		t.Fatalf("voiding: %v", err)
	}
	if v.CookEventID != nil {
		t.Errorf("CookEventID = %v, want nil", *v.CookEventID)
	}
	if v.ServingsReturned != 0 {
		t.Errorf("ServingsReturned = %v, want 0", v.ServingsReturned)
	}
}
