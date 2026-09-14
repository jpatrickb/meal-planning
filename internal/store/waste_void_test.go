package store

import (
	"math"
	"testing"
)

// Voiding an item-waste entry has to put the stock back on hand, onto the
// lot it was discarded from, so the lot keeps its price and expiration.
func TestVoidWasteEntryRestoresDiscardedStock(t *testing.T) {
	db := newTestDB(t)
	itemID, err := CreatePantryItem(db, "Ham (sliced)", "protein_meat", "lb")
	if err != nil {
		t.Fatalf("creating item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, unit_cost, acquired_date, expires_date, status)
		VALUES (67, ?, 1, 'lb', 4.96, '2026-08-22', '2026-08-29', 'on_hand')`, itemID)

	wasteID, err := LogItemWaste(db, itemID, 1, "lb", "spoiled", "2026-08-29", nil, nil, nil)
	if err != nil {
		t.Fatalf("logging waste: %v", err)
	}

	v, err := VoidWasteEntry(db, wasteID)
	if err != nil {
		t.Fatalf("voiding waste: %v", err)
	}
	if len(v.RestoredLots) != 1 || v.RestoredLots[0].StockID != 67 {
		t.Fatalf("restored %+v, want the original lot 67", v.RestoredLots)
	}
	if v.Unrestored > 1e-9 {
		t.Errorf("Unrestored = %v, want everything placed", v.Unrestored)
	}

	var qty, cost float64
	var expires, status string
	if err := db.QueryRow(`SELECT quantity, unit_cost, expires_date, status FROM pantry_stock WHERE id = 67`).
		Scan(&qty, &cost, &expires, &status); err != nil {
		t.Fatalf("reading lot: %v", err)
	}
	if math.Abs(qty-1) > 1e-9 || status != "on_hand" {
		t.Errorf("lot = %v %q, want 1 on_hand", qty, status)
	}
	if cost != 4.96 || expires != "2026-08-29" {
		t.Errorf("lot identity changed: cost %v expires %q", cost, expires)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM waste_log WHERE id = ?`, wasteID).Scan(&n); err != nil {
		t.Fatalf("counting waste rows: %v", err)
	}
	if n != 0 {
		t.Errorf("waste row still present, want it deleted")
	}
}

// Voiding a leftover-waste entry gives the servings back to the cook event.
func TestVoidWasteEntryReturnsLeftoverServings(t *testing.T) {
	db := newTestDB(t)
	mustExec(t, db, `INSERT INTO recipes (id, title, category, raw_json, synced_at) VALUES ('r1','R1','mains','{}','2026-08-22')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	cookID, err := InsertCookEvent(tx, CookEvent{
		RecipeID: "r1", CookedDate: "2026-08-23", ServingsYielded: 4, TotalCost: 8.00,
	})
	if err != nil {
		t.Fatalf("inserting cook event: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	wasteID, err := LogLeftoverWaste(db, cookID, 2, "too_much_cooked", "2026-08-27", nil, nil)
	if err != nil {
		t.Fatalf("logging leftover waste: %v", err)
	}
	var remaining float64
	if err := db.QueryRow(`SELECT servings_remaining FROM cook_events WHERE id = ?`, cookID).Scan(&remaining); err != nil {
		t.Fatalf("reading cook event: %v", err)
	}
	if math.Abs(remaining-2) > 1e-9 {
		t.Fatalf("servings_remaining = %v after waste, want 2", remaining)
	}

	v, err := VoidWasteEntry(db, wasteID)
	if err != nil {
		t.Fatalf("voiding waste: %v", err)
	}
	if math.Abs(v.ServingsReturned-2) > 1e-9 {
		t.Errorf("ServingsReturned = %v, want 2", v.ServingsReturned)
	}
	if err := db.QueryRow(`SELECT servings_remaining FROM cook_events WHERE id = ?`, cookID).Scan(&remaining); err != nil {
		t.Fatalf("reading cook event: %v", err)
	}
	if math.Abs(remaining-4) > 1e-9 {
		t.Errorf("servings_remaining = %v, want the full 4 back", remaining)
	}
}

func TestVoidWasteEntryRejectsUnknownID(t *testing.T) {
	db := newTestDB(t)
	if _, err := VoidWasteEntry(db, 999); err == nil {
		t.Fatal("voiding an unknown waste id succeeded, want an error")
	}
}
