package store

import "database/sql"

// MealPlanEntry is one planned meal slot.
type MealPlanEntry struct {
	ID           int64
	PlanDate     string
	MealSlot     string
	RecipeID     *string
	RecipeTitle  string // joined in, empty if RecipeID is nil or on inserts
	RecipeEmoji  string // joined in, empty if RecipeID is nil or on inserts
	FreeTextMeal *string
	PlannedFor   string
	Status       string
}

// UpsertPlanEntry sets (or replaces) the plan for a given date+slot, per the
// UNIQUE(plan_date, meal_slot) constraint.
func UpsertPlanEntry(db *sql.DB, e MealPlanEntry) (int64, error) {
	_, err := db.Exec(`
		INSERT INTO meal_plan_entries (plan_date, meal_slot, recipe_id, free_text_meal, planned_for, status)
		VALUES (?, ?, ?, ?, ?, 'planned')
		ON CONFLICT(plan_date, meal_slot) DO UPDATE SET
			recipe_id = excluded.recipe_id,
			free_text_meal = excluded.free_text_meal,
			planned_for = excluded.planned_for,
			status = 'planned'
	`, e.PlanDate, e.MealSlot, e.RecipeID, e.FreeTextMeal, e.PlannedFor)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM meal_plan_entries WHERE plan_date = ? AND meal_slot = ?`, e.PlanDate, e.MealSlot).Scan(&id)
	return id, err
}

// ListPlanEntries returns planned meals in [from, to] (inclusive), joined
// with recipe title where applicable, ordered by date then a fixed slot order.
func ListPlanEntries(db *sql.DB, from, to string) ([]MealPlanEntry, error) {
	rows, err := db.Query(`
		SELECT mpe.id, mpe.plan_date, mpe.meal_slot, mpe.recipe_id, COALESCE(r.title, ''),
		       COALESCE(r.emoji, ''), mpe.free_text_meal, mpe.planned_for, mpe.status
		FROM meal_plan_entries mpe
		LEFT JOIN recipes r ON r.id = mpe.recipe_id
		WHERE mpe.plan_date BETWEEN ? AND ?
		ORDER BY mpe.plan_date,
			CASE mpe.meal_slot WHEN 'breakfast' THEN 0 WHEN 'lunch' THEN 1 WHEN 'dinner' THEN 2 ELSE 3 END
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MealPlanEntry
	for rows.Next() {
		var e MealPlanEntry
		if err := rows.Scan(&e.ID, &e.PlanDate, &e.MealSlot, &e.RecipeID, &e.RecipeTitle, &e.RecipeEmoji, &e.FreeTextMeal, &e.PlannedFor, &e.Status); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListPlannedRecipeIDs returns the distinct recipe ids planned in [from, to],
// for shopping-list build.
func ListPlannedRecipeIDs(db *sql.DB, from, to string) ([]string, error) {
	rows, err := db.Query(`
		SELECT DISTINCT recipe_id FROM meal_plan_entries
		WHERE plan_date BETWEEN ? AND ? AND recipe_id IS NOT NULL
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
