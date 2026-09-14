package store

import (
	"database/sql"
	"fmt"
)

// ShoppingListItem is one line on the checklist.
type ShoppingListItem struct {
	ID             int64
	Description    string
	SourceRecipeID *string
	Checked        bool
	CreatedAt      string
}

// AddShoppingListItem adds one manual or recipe-sourced item.
func AddShoppingListItem(db *sql.DB, description string, sourceRecipeID *string) (int64, error) {
	res, err := db.Exec(`INSERT INTO shopping_list_items (description, source_recipe_id) VALUES (?, ?)`, description, sourceRecipeID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RebuildRecipeShoppingListItems reconciles a recipe's seeded lines against
// its current ingredient list: unchecked lines no longer in the recipe are
// removed, missing lines are added, and anything already present - checked
// OR unchecked - is left alone. That last part matters: a naive "delete
// unchecked, reinsert everything" would resurrect an unchecked duplicate of
// a line the user already checked off. Manually-added items
// (source_recipe_id NULL) are never touched.
func RebuildRecipeShoppingListItems(db *sql.DB, recipeID string, ingredientLines []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, description, checked FROM shopping_list_items WHERE source_recipe_id = ?`, recipeID)
	if err != nil {
		return err
	}
	type existingRow struct {
		id      int64
		desc    string
		checked bool
	}
	var existing []existingRow
	for rows.Next() {
		var r existingRow
		if err := rows.Scan(&r.id, &r.desc, &r.checked); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	existingDescs := map[string]bool{}
	for _, r := range existing {
		existingDescs[r.desc] = true
	}
	wanted := map[string]bool{}
	for _, l := range ingredientLines {
		wanted[l] = true
	}

	for _, r := range existing {
		if !r.checked && !wanted[r.desc] {
			if _, err := tx.Exec(`DELETE FROM shopping_list_items WHERE id = ?`, r.id); err != nil {
				return err
			}
		}
	}
	for _, line := range ingredientLines {
		if existingDescs[line] {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO shopping_list_items (description, source_recipe_id) VALUES (?, ?)`, line, recipeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListShoppingListItems returns items, optionally including checked ones.
func ListShoppingListItems(db *sql.DB, includeChecked bool) ([]ShoppingListItem, error) {
	query := `SELECT id, description, source_recipe_id, checked, created_at FROM shopping_list_items`
	if !includeChecked {
		query += ` WHERE checked = 0`
	}
	query += ` ORDER BY source_recipe_id IS NULL, source_recipe_id, id`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ShoppingListItem
	for rows.Next() {
		var it ShoppingListItem
		if err := rows.Scan(&it.ID, &it.Description, &it.SourceRecipeID, &it.Checked, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CheckShoppingListItem marks an item checked (bought).
func CheckShoppingListItem(db *sql.DB, id int64) error {
	res, err := db.Exec(`UPDATE shopping_list_items SET checked = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClearShoppingList removes every item, checked or not.
func ClearShoppingList(db *sql.DB) (int64, error) {
	res, err := db.Exec(`DELETE FROM shopping_list_items`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteShoppingListItem removes one item outright, for a line that was
// added by mistake. Distinct from CheckShoppingListItem, which means "bought
// it" - recording a purchase that never happened just to hide a bad line
// would quietly corrupt the list's history.
func DeleteShoppingListItem(db *sql.DB, id int64) error {
	res, err := db.Exec(`DELETE FROM shopping_list_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no shopping list item with id %d", id)
	}
	return nil
}
