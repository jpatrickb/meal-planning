package store

import "database/sql"

// CookEventIngredient is one pantry lot actually decremented for one
// ingredient at one cook. One row per lot, not per ingredient, since FIFO
// consumption of a single ingredient can span multiple lots.
type CookEventIngredient struct {
	ID            int64
	CookEventID   int64
	ItemID        int64
	PantryStockID int64
	Quantity      float64
	Unit          string
	Source        string // "recipe_default" or "override"
}

// InsertCookEventIngredient records one lot's contribution to an
// ingredient's consumption at a cook event. Must run inside tx.
func InsertCookEventIngredient(tx *sql.Tx, ci CookEventIngredient) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO cook_event_ingredients (cook_event_id, item_id, pantry_stock_id, quantity, unit, source)
		VALUES (?, ?, ?, ?, ?, ?)
	`, ci.CookEventID, ci.ItemID, ci.PantryStockID, ci.Quantity, ci.Unit, ci.Source)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCookEventIngredients returns every lot consumed for a cook event.
func ListCookEventIngredients(db *sql.DB, cookEventID int64) ([]CookEventIngredient, error) {
	rows, err := db.Query(`
		SELECT id, cook_event_id, item_id, pantry_stock_id, quantity, unit, source
		FROM cook_event_ingredients WHERE cook_event_id = ? ORDER BY id
	`, cookEventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CookEventIngredient
	for rows.Next() {
		var ci CookEventIngredient
		if err := rows.Scan(&ci.ID, &ci.CookEventID, &ci.ItemID, &ci.PantryStockID, &ci.Quantity, &ci.Unit, &ci.Source); err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, rows.Err()
}
