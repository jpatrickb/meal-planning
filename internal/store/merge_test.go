package store

import (
	"database/sql"
	"testing"
)

// newTestDB builds a real migrated database in a temp dir, so merge is
// exercised against the actual schema rather than a hand-rolled subset.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustQueryRow(t *testing.T, db *sql.DB, query string, args ...any) *sql.Row {
	t.Helper()
	return db.QueryRow(query, args...)
}

func TestMergePantryItemsRepointsEverythingAndDeletesDuplicate(t *testing.T) {
	db := newTestDB(t)

	keepID, err := CreatePantryItem(db, "Heavy Cream", "dairy", "cup")
	if err != nil {
		t.Fatalf("creating keeper: %v", err)
	}
	dupID, err := CreatePantryItem(db, "Whipping Cream", "dairy", "cup")
	if err != nil {
		t.Fatalf("creating duplicate: %v", err)
	}

	mustExec(t, db, `INSERT INTO pantry_stock (item_id, quantity, unit, acquired_date) VALUES (?, 2, 'cup', '2026-08-22')`, dupID)
	mustExec(t, db, `INSERT INTO recipes (id, title, category, raw_json, synced_at) VALUES ('r1','R1','mains','{}', '2026-08-22')`)
	mustExec(t, db, `INSERT INTO recipe_ingredients (recipe_id, raw_ingredient_text, status, item_id, quantity, unit, quantity_source)
		VALUES ('r1','1 cup cream','mapped',?,1,'cup','recipe_stated')`, dupID)
	mustExec(t, db, `INSERT INTO item_aliases (raw_text_normalized, item_id) VALUES ('gv whipping cream', ?)`, dupID)
	mustExec(t, db, `INSERT INTO typical_shelf_life (item_id, days, source) VALUES (?, 14, 'estimated')`, dupID)

	res, err := MergePantryItems(db, dupID, keepID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.StockLots != 1 || res.RecipeLines != 1 || res.Aliases != 1 {
		t.Errorf("moved stock=%d recipes=%d aliases=%d, want 1/1/1", res.StockLots, res.RecipeLines, res.Aliases)
	}

	// The duplicate is gone and nothing still points at it.
	var items int
	mustQueryRow(t, db, `SELECT COUNT(*) FROM pantry_items WHERE id = ?`, dupID).Scan(&items)
	if items != 0 {
		t.Errorf("duplicate item still present")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM pantry_stock WHERE item_id = ?`,
		`SELECT COUNT(*) FROM recipe_ingredients WHERE item_id = ?`,
		`SELECT COUNT(*) FROM item_aliases WHERE item_id = ?`,
		`SELECT COUNT(*) FROM typical_shelf_life WHERE item_id = ?`,
	} {
		var n int
		mustQueryRow(t, db, q, dupID).Scan(&n)
		if n != 0 {
			t.Errorf("%s still returned %d rows for the merged-away item", q, n)
		}
	}

	var keptStock float64
	mustQueryRow(t, db, `SELECT COALESCE(SUM(quantity),0) FROM pantry_stock WHERE item_id = ?`, keepID).Scan(&keptStock)
	if keptStock != 2 {
		t.Errorf("kept item has %.1f stock, want 2", keptStock)
	}
}

func TestMergePantryItemsKeepsSurvivorsShelfLifeOnConflict(t *testing.T) {
	db := newTestDB(t)

	keepID, _ := CreatePantryItem(db, "Cherry Tomatoes", "veg_fresh_frozen", "pint")
	dupID, _ := CreatePantryItem(db, "Cherubs Grape Tomatoes", "veg_fresh_frozen", "oz")
	mustExec(t, db, `INSERT INTO typical_shelf_life (item_id, days, source) VALUES (?, 7, 'patrick_provided')`, keepID)
	mustExec(t, db, `INSERT INTO typical_shelf_life (item_id, days, source) VALUES (?, 99, 'estimated')`, dupID)

	res, err := MergePantryItems(db, dupID, keepID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.ShelfLifeDropped {
		t.Error("expected the duplicate's shelf life to be reported as dropped")
	}
	var days int
	mustQueryRow(t, db, `SELECT days FROM typical_shelf_life WHERE item_id = ?`, keepID).Scan(&days)
	if days != 7 {
		t.Errorf("shelf life is %d days, want the survivor's 7", days)
	}
}

func TestMergePantryItemsRejectsSelfMerge(t *testing.T) {
	db := newTestDB(t)
	id, _ := CreatePantryItem(db, "Butter", "dairy", "cup")
	if _, err := MergePantryItems(db, id, id); err == nil {
		t.Error("merging an item into itself should be an error")
	}
}

func TestMergePantryItemsRejectsMissingItem(t *testing.T) {
	db := newTestDB(t)
	id, _ := CreatePantryItem(db, "Butter", "dairy", "cup")
	if _, err := MergePantryItems(db, 999999, id); err == nil {
		t.Error("merging a nonexistent item should be an error")
	}
}

func TestVoidLogEntryDeletesTheRowAndReportsIt(t *testing.T) {
	db := newTestDB(t)
	mustExec(t, db, `INSERT INTO consumption_log (id, consumed_date, person, meal_slot, source, free_text_food, servings, cost)
		VALUES (7, '2026-08-23', 'patrick', 'breakfast', 'from_pantry', 'French toast', 1, 27.97)`)

	v, err := VoidLogEntry(db, 7)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if v.Cost != 27.97 || v.Person != "patrick" || v.Food != "French toast" {
		t.Errorf("reported %+v, want the row's own values back", v)
	}
	var n int
	mustQueryRow(t, db, `SELECT COUNT(*) FROM consumption_log WHERE id = 7`).Scan(&n)
	if n != 0 {
		t.Error("row still present after void")
	}
}

func TestVoidLogEntryRejectsUnknownID(t *testing.T) {
	db := newTestDB(t)
	if _, err := VoidLogEntry(db, 4242); err == nil {
		t.Error("voiding a nonexistent entry should be an error")
	}
}
