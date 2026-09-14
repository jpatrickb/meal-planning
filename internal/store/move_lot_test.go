package store

import (
	"math"
	"testing"
)

// Moving a lot must keep everything that makes it that lot - quantity, unit,
// price, acquired date, expiration and status - because the whole point is
// rescuing a mis-matched receipt line without inventing a replacement.
func TestMoveStockLotKeepsLotIdentity(t *testing.T) {
	db := newTestDB(t)
	from, err := CreatePantryItem(db, "Pasta", "grains", "oz")
	if err != nil {
		t.Fatalf("creating source item: %v", err)
	}
	to, err := CreatePantryItem(db, "Medium Shell Pasta", "grains", "oz")
	if err != nil {
		t.Fatalf("creating target item: %v", err)
	}
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, unit_cost, acquired_date, expires_date, status)
		VALUES (69, ?, 16, 'oz', 0.06125, '2026-08-22', '2027-08-22', 'on_hand')`, from)
	// A second lot that must not move.
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, acquired_date, status)
		VALUES (15, ?, 8, 'oz', '2026-08-20', 'on_hand')`, from)

	m, err := MoveStockLot(db, 69, to)
	if err != nil {
		t.Fatalf("moving lot: %v", err)
	}
	if m.FromItemID != from || m.ToItemID != to {
		t.Errorf("moved %d -> %d, want %d -> %d", m.FromItemID, m.ToItemID, from, to)
	}
	if m.UnitMismatch != "" {
		t.Errorf("UnitMismatch = %q, want none (both oz)", m.UnitMismatch)
	}

	var itemID int64
	var qty, cost float64
	var unit, acquired, expires, status string
	if err := db.QueryRow(`SELECT item_id, quantity, unit, unit_cost, acquired_date, expires_date, status
		FROM pantry_stock WHERE id = 69`).Scan(&itemID, &qty, &unit, &cost, &acquired, &expires, &status); err != nil {
		t.Fatalf("reading moved lot: %v", err)
	}
	if itemID != to {
		t.Errorf("item_id = %d, want %d", itemID, to)
	}
	if math.Abs(qty-16) > 1e-9 || unit != "oz" || cost != 0.06125 {
		t.Errorf("lot contents changed: %v %s @ %v", qty, unit, cost)
	}
	if acquired != "2026-08-22" || expires != "2027-08-22" || status != "on_hand" {
		t.Errorf("lot dates/status changed: acquired %q expires %q status %q", acquired, expires, status)
	}

	// The other lot stayed put.
	if err := db.QueryRow(`SELECT item_id FROM pantry_stock WHERE id = 15`).Scan(&itemID); err != nil {
		t.Fatalf("reading untouched lot: %v", err)
	}
	if itemID != from {
		t.Errorf("lot 15 moved to %d, want it left on %d", itemID, from)
	}
}

// A lot whose unit differs from the target item's default still moves, but
// the caller is told, because a decrement in the item's unit won't reach it
// until a conversion exists.
func TestMoveStockLotReportsUnitMismatch(t *testing.T) {
	db := newTestDB(t)
	from, _ := CreatePantryItem(db, "Pasta", "grains", "oz")
	to, _ := CreatePantryItem(db, "Medium Shell Pasta", "grains", "box")
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, acquired_date, status)
		VALUES (69, ?, 16, 'oz', '2026-08-22', 'on_hand')`, from)

	m, err := MoveStockLot(db, 69, to)
	if err != nil {
		t.Fatalf("moving lot: %v", err)
	}
	if m.UnitMismatch != "box" {
		t.Errorf("UnitMismatch = %q, want \"box\"", m.UnitMismatch)
	}
}

func TestMoveStockLotRejectsBadInput(t *testing.T) {
	db := newTestDB(t)
	item, _ := CreatePantryItem(db, "Pasta", "grains", "oz")
	mustExec(t, db, `INSERT INTO pantry_stock (id, item_id, quantity, unit, acquired_date, status)
		VALUES (1, ?, 5, 'oz', '2026-08-22', 'on_hand')`, item)

	if _, err := MoveStockLot(db, 999, item); err == nil {
		t.Error("moving an unknown lot succeeded, want an error")
	}
	if _, err := MoveStockLot(db, 1, 999); err == nil {
		t.Error("moving to an unknown item succeeded, want an error")
	}
	if _, err := MoveStockLot(db, 1, item); err == nil {
		t.Error("moving a lot onto its own item succeeded, want an error")
	}
}
