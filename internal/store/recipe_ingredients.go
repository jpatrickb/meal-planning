package store

import "database/sql"

// RecipeIngredient is one raw ingredient line's mapping to a pantry item, or
// an explicit not_tracked disposition (e.g. "salt to taste" isn't worth
// pantry-tracking).
type RecipeIngredient struct {
	ID                int64
	RecipeID          string
	IngredientGroup   *string
	RawIngredientText string
	Status            string // "mapped" or "not_tracked"
	ItemID            *int64
	Quantity          *float64
	Unit              *string
	QuantitySource    *string // "recipe_stated" or "estimated"; nil iff not_tracked
	ConfirmedAt       string
}

const recipeIngredientColumns = `id, recipe_id, ingredient_group, raw_ingredient_text, status, item_id, quantity, unit, quantity_source, confirmed_at`

func scanRecipeIngredient(row interface{ Scan(dest ...any) error }) (RecipeIngredient, error) {
	var ri RecipeIngredient
	err := row.Scan(&ri.ID, &ri.RecipeID, &ri.IngredientGroup, &ri.RawIngredientText, &ri.Status,
		&ri.ItemID, &ri.Quantity, &ri.Unit, &ri.QuantitySource, &ri.ConfirmedAt)
	return ri, err
}

// ListRecipeIngredients returns every mapped/not_tracked row for a recipe.
func ListRecipeIngredients(db *sql.DB, recipeID string) ([]RecipeIngredient, error) {
	rows, err := db.Query(`SELECT `+recipeIngredientColumns+` FROM recipe_ingredients WHERE recipe_id = ? ORDER BY id`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecipeIngredient
	for rows.Next() {
		ri, err := scanRecipeIngredient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

// GetRecipeIngredientByLine looks up the existing row for one raw ingredient
// line, matched by group + exact text, if any.
func GetRecipeIngredientByLine(db *sql.DB, recipeID string, group *string, rawText string) (RecipeIngredient, bool, error) {
	var row *sql.Row
	if group == nil {
		row = db.QueryRow(`SELECT `+recipeIngredientColumns+` FROM recipe_ingredients WHERE recipe_id = ? AND ingredient_group IS NULL AND raw_ingredient_text = ?`, recipeID, rawText)
	} else {
		row = db.QueryRow(`SELECT `+recipeIngredientColumns+` FROM recipe_ingredients WHERE recipe_id = ? AND ingredient_group = ? AND raw_ingredient_text = ?`, recipeID, *group, rawText)
	}
	ri, err := scanRecipeIngredient(row)
	if err == sql.ErrNoRows {
		return RecipeIngredient{}, false, nil
	}
	return ri, err == nil, err
}

// UpsertRecipeIngredient inserts a new mapping row, or updates the existing
// one for the same (recipe_id, ingredient_group, raw_ingredient_text) line --
// letting a mistake be corrected by simply re-running `map-ingredients` on
// the same line rather than needing a separate edit command.
func UpsertRecipeIngredient(db *sql.DB, ri RecipeIngredient) (int64, error) {
	existing, ok, err := GetRecipeIngredientByLine(db, ri.RecipeID, ri.IngredientGroup, ri.RawIngredientText)
	if err != nil {
		return 0, err
	}
	if ok {
		_, err := db.Exec(`
			UPDATE recipe_ingredients SET status = ?, item_id = ?, quantity = ?, unit = ?, quantity_source = ?, confirmed_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, ri.Status, ri.ItemID, ri.Quantity, ri.Unit, ri.QuantitySource, existing.ID)
		return existing.ID, err
	}
	res, err := db.Exec(`
		INSERT INTO recipe_ingredients (recipe_id, ingredient_group, raw_ingredient_text, status, item_id, quantity, unit, quantity_source, confirmed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, ri.RecipeID, ri.IngredientGroup, ri.RawIngredientText, ri.Status, ri.ItemID, ri.Quantity, ri.Unit, ri.QuantitySource)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
