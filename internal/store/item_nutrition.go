package store

import "database/sql"

// ItemNutrition is per-100g macro data recorded directly for a pantry item,
// from the product's own label. It outranks the item's USDA link everywhere
// macros are computed: for a branded product the package in hand is simply
// better evidence than the nearest generic entry USDA happens to have.
type ItemNutrition struct {
	ItemID          int64
	CaloriesPer100g float64
	ProteinPer100g  float64
	CarbsPer100g    float64
	FatPer100g      float64
	FiberPer100g    float64
	Source          string
	Note            *string
	UpdatedAt       string
}

// UpsertItemNutrition records (or replaces) an item's label nutrition.
func UpsertItemNutrition(db *sql.DB, n ItemNutrition) error {
	_, err := db.Exec(`
		INSERT INTO item_nutrition (
			item_id, calories_per_100g, protein_per_100g, carbs_per_100g,
			fat_per_100g, fiber_per_100g, source, note, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(item_id) DO UPDATE SET
			calories_per_100g = excluded.calories_per_100g,
			protein_per_100g = excluded.protein_per_100g,
			carbs_per_100g = excluded.carbs_per_100g,
			fat_per_100g = excluded.fat_per_100g,
			fiber_per_100g = excluded.fiber_per_100g,
			source = excluded.source,
			note = excluded.note,
			updated_at = CURRENT_TIMESTAMP
	`, n.ItemID, n.CaloriesPer100g, n.ProteinPer100g, n.CarbsPer100g, n.FatPer100g,
		n.FiberPer100g, n.Source, n.Note)
	return err
}

// GetItemNutrition returns an item's label nutrition, if any was recorded.
func GetItemNutrition(db *sql.DB, itemID int64) (ItemNutrition, bool, error) {
	var n ItemNutrition
	err := db.QueryRow(`
		SELECT item_id, calories_per_100g, protein_per_100g, carbs_per_100g,
		       fat_per_100g, fiber_per_100g, source, note, updated_at
		FROM item_nutrition WHERE item_id = ?
	`, itemID).Scan(&n.ItemID, &n.CaloriesPer100g, &n.ProteinPer100g, &n.CarbsPer100g,
		&n.FatPer100g, &n.FiberPer100g, &n.Source, &n.Note, &n.UpdatedAt)
	if err == sql.ErrNoRows {
		return ItemNutrition{}, false, nil
	}
	if err != nil {
		return ItemNutrition{}, false, err
	}
	return n, true, nil
}

// ClearItemNutrition drops an item's label nutrition, falling back to its
// USDA link.
func ClearItemNutrition(db *sql.DB, itemID int64) error {
	_, err := db.Exec(`DELETE FROM item_nutrition WHERE item_id = ?`, itemID)
	return err
}
