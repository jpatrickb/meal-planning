package store

import (
	"database/sql"
	"fmt"
)

// MergeResult reports what a MergePantryItems call moved.
type MergeResult struct {
	FromID, IntoID   int64
	FromName         string
	IntoName         string
	StockLots        int64
	PurchaseItems    int64
	Aliases          int64
	WasteRows        int64
	RecipeLines      int64
	CookEventLines   int64
	ShelfLifeDropped bool
	ConversionsMoved int64
}

// MergePantryItems folds a duplicate pantry item into the one that should
// survive, repointing every table that references it and then deleting the
// duplicate. Both items must exist and differ.
//
// Everything runs in one transaction: a half-merged item would leave recipes
// pointing at a row that no longer exists, which is worse than not merging.
//
// Rows that would collide on a UNIQUE constraint after repointing (an alias
// already recorded against the surviving item, a shelf-life estimate, a
// duplicate unit conversion) are dropped rather than moved, since the
// surviving item's own value is the one to keep.
func MergePantryItems(db *sql.DB, fromID, intoID int64) (MergeResult, error) {
	res := MergeResult{FromID: fromID, IntoID: intoID}
	if fromID == intoID {
		return res, fmt.Errorf("cannot merge item %d into itself", fromID)
	}

	from, err := GetPantryItem(db, fromID)
	if err != nil {
		return res, fmt.Errorf("loading item %d: %w", fromID, err)
	}
	into, err := GetPantryItem(db, intoID)
	if err != nil {
		return res, fmt.Errorf("loading item %d: %w", intoID, err)
	}
	res.FromName, res.IntoName = from.Name, into.Name

	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	// Plain repoints: these tables have no uniqueness constraint on the item
	// reference. purchase_items names its column matched_item_id, not item_id.
	for _, m := range []struct {
		table  string
		column string
		count  *int64
	}{
		{"pantry_stock", "item_id", &res.StockLots},
		{"purchase_items", "matched_item_id", &res.PurchaseItems},
		{"waste_log", "item_id", &res.WasteRows},
		{"recipe_ingredients", "item_id", &res.RecipeLines},
		{"cook_event_ingredients", "item_id", &res.CookEventLines},
	} {
		r, err := tx.Exec(`UPDATE `+m.table+` SET `+m.column+` = ? WHERE `+m.column+` = ?`, intoID, fromID)
		if err != nil {
			return res, fmt.Errorf("repointing %s: %w", m.table, err)
		}
		n, _ := r.RowsAffected()
		*m.count = n
	}

	// item_aliases has two UNIQUE columns, raw_text_normalized and upc, so an
	// alias only moves when it collides with neither. Anything already claimed
	// by the surviving item stays with it and the duplicate's copy is dropped.
	r, err := tx.Exec(`
		UPDATE item_aliases SET item_id = ? WHERE item_id = ?
		AND raw_text_normalized NOT IN (
			SELECT raw_text_normalized FROM item_aliases WHERE item_id = ?)
		AND (upc IS NULL OR upc NOT IN (
			SELECT upc FROM item_aliases WHERE item_id = ? AND upc IS NOT NULL))`,
		intoID, fromID, intoID, intoID)
	if err != nil {
		return res, fmt.Errorf("repointing item_aliases: %w", err)
	}
	res.Aliases, _ = r.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM item_aliases WHERE item_id = ?`, fromID); err != nil {
		return res, fmt.Errorf("clearing leftover aliases: %w", err)
	}

	// Unit conversions are keyed by (item_id, from_unit, to_unit); keep the
	// surviving item's factor where both define the same pair.
	r, err = tx.Exec(`
		UPDATE unit_conversions SET item_id = ? WHERE item_id = ?
		AND NOT EXISTS (
			SELECT 1 FROM unit_conversions u2
			WHERE u2.item_id = ? AND u2.from_unit = unit_conversions.from_unit
			  AND u2.to_unit = unit_conversions.to_unit)`,
		intoID, fromID, intoID)
	if err != nil {
		return res, fmt.Errorf("repointing unit_conversions: %w", err)
	}
	res.ConversionsMoved, _ = r.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM unit_conversions WHERE item_id = ?`, fromID); err != nil {
		return res, fmt.Errorf("clearing leftover conversions: %w", err)
	}

	// Shelf life is one row per item; the surviving item's estimate stands.
	var intoHasShelfLife int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM typical_shelf_life WHERE item_id = ?`, intoID).Scan(&intoHasShelfLife); err != nil {
		return res, err
	}
	if intoHasShelfLife == 0 {
		if _, err := tx.Exec(`UPDATE typical_shelf_life SET item_id = ? WHERE item_id = ?`, intoID, fromID); err != nil {
			return res, fmt.Errorf("repointing typical_shelf_life: %w", err)
		}
	} else {
		r, err := tx.Exec(`DELETE FROM typical_shelf_life WHERE item_id = ?`, fromID)
		if err != nil {
			return res, fmt.Errorf("dropping duplicate shelf life: %w", err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.ShelfLifeDropped = true
		}
	}

	if _, err := tx.Exec(`DELETE FROM pantry_items WHERE id = ?`, fromID); err != nil {
		return res, fmt.Errorf("deleting merged item %d: %w", fromID, err)
	}
	return res, tx.Commit()
}
