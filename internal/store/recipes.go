package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Recipe mirrors the recipes table: content synced read-only from
// recipes.patrickandthea.com. Never written to by anything except sync.
type Recipe struct {
	ID               string
	Title            string
	Emoji            string
	Category         string
	ServesRaw        string
	ImageURL         string
	DateAdded        string
	Active           bool
	Tags             []string
	IngredientGroups []IngredientGroup
	Instructions     []string
	Notes            []string
	RelatedRecipes   []string
	RawJSON          string
	SyncedAt         string
}

// IngredientGroup mirrors one entry of the site's ingredientGroups array.
type IngredientGroup struct {
	Heading *string  `json:"heading"`
	Items   []string `json:"items"`
}

// RecipeMeta is LOCAL, owned data keyed by recipe id. Sync never touches it.
type RecipeMeta struct {
	RecipeID         string
	ServesOverride   *int
	ServingsAreMeals bool
	CostTotal        *float64
	MacrosPerServing *MacrosPerServing
	Rating           *int
	Notes            *string
	UpdatedAt        string
}

// MacrosPerServing is stored as JSON in recipe_meta.macros_per_serving_json.
type MacrosPerServing struct {
	Calories float64 `json:"calories"`
	Protein  float64 `json:"protein"`
	Carbs    float64 `json:"carbs"`
	Fat      float64 `json:"fat"`
	Fiber    float64 `json:"fiber"`
}

// UpsertRecipe inserts or updates a recipe row by id. Must run inside tx so
// callers can batch a full sync atomically.
func UpsertRecipe(tx *sql.Tx, r Recipe) error {
	tags, err := json.Marshal(r.Tags)
	if err != nil {
		return fmt.Errorf("marshaling tags: %w", err)
	}
	groups, err := json.Marshal(r.IngredientGroups)
	if err != nil {
		return fmt.Errorf("marshaling ingredient groups: %w", err)
	}
	instructions, err := json.Marshal(r.Instructions)
	if err != nil {
		return fmt.Errorf("marshaling instructions: %w", err)
	}
	notes, err := json.Marshal(r.Notes)
	if err != nil {
		return fmt.Errorf("marshaling notes: %w", err)
	}
	related, err := json.Marshal(r.RelatedRecipes)
	if err != nil {
		return fmt.Errorf("marshaling related recipes: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO recipes (
			id, title, emoji, category, serves_raw, image_url, date_added, active,
			tags_json, ingredient_groups_json, instructions_json, notes_json,
			related_recipes_json, raw_json, synced_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			emoji = excluded.emoji,
			category = excluded.category,
			serves_raw = excluded.serves_raw,
			image_url = excluded.image_url,
			date_added = excluded.date_added,
			active = 1,
			tags_json = excluded.tags_json,
			ingredient_groups_json = excluded.ingredient_groups_json,
			instructions_json = excluded.instructions_json,
			notes_json = excluded.notes_json,
			related_recipes_json = excluded.related_recipes_json,
			raw_json = excluded.raw_json,
			synced_at = excluded.synced_at
	`, r.ID, r.Title, r.Emoji, r.Category, r.ServesRaw, r.ImageURL, r.DateAdded,
		string(tags), string(groups), string(instructions), string(notes),
		string(related), r.RawJSON, r.SyncedAt)
	return err
}

// DeactivateMissingRecipes marks every recipe row inactive; sync then
// reactivates each one it upserts. Recipes that drop out of the feed keep
// their row (and recipe_meta) but stop showing in default listings - never
// hard-deleted, since cook_events/consumption_log reference them by id.
func DeactivateMissingRecipes(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE recipes SET active = 0`)
	return err
}

func scanRecipe(row interface {
	Scan(dest ...any) error
}) (Recipe, error) {
	var r Recipe
	var tags, groups, instructions, notes, related string
	err := row.Scan(
		&r.ID, &r.Title, &r.Emoji, &r.Category, &r.ServesRaw, &r.ImageURL,
		&r.DateAdded, &r.Active, &tags, &groups, &instructions, &notes,
		&related, &r.RawJSON, &r.SyncedAt,
	)
	if err != nil {
		return Recipe{}, err
	}
	if err := json.Unmarshal([]byte(tags), &r.Tags); err != nil {
		return Recipe{}, fmt.Errorf("unmarshaling tags for %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(groups), &r.IngredientGroups); err != nil {
		return Recipe{}, fmt.Errorf("unmarshaling ingredient groups for %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(instructions), &r.Instructions); err != nil {
		return Recipe{}, fmt.Errorf("unmarshaling instructions for %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(notes), &r.Notes); err != nil {
		return Recipe{}, fmt.Errorf("unmarshaling notes for %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(related), &r.RelatedRecipes); err != nil {
		return Recipe{}, fmt.Errorf("unmarshaling related recipes for %s: %w", r.ID, err)
	}
	return r, nil
}

const recipeColumns = `id, title, emoji, category, serves_raw, image_url, date_added, active,
	tags_json, ingredient_groups_json, instructions_json, notes_json,
	related_recipes_json, raw_json, synced_at`

// ListRecipesOpts filters the recipe listing.
type ListRecipesOpts struct {
	Category        string
	Tag             string
	IncludeInactive bool
}

// ListRecipes returns recipes matching opts, ordered by title.
func ListRecipes(db *sql.DB, opts ListRecipesOpts) ([]Recipe, error) {
	query := `SELECT ` + recipeColumns + ` FROM recipes WHERE 1=1`
	var args []any
	if !opts.IncludeInactive {
		query += ` AND active = 1`
	}
	if opts.Category != "" {
		query += ` AND category = ?`
		args = append(args, opts.Category)
	}
	if opts.Tag != "" {
		query += ` AND tags_json LIKE ?`
		args = append(args, `%"`+opts.Tag+`"%`)
	}
	query += ` ORDER BY title`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recipe
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRecipe fetches a single recipe by id. Returns sql.ErrNoRows if absent.
func GetRecipe(db *sql.DB, id string) (Recipe, error) {
	row := db.QueryRow(`SELECT `+recipeColumns+` FROM recipes WHERE id = ?`, id)
	return scanRecipe(row)
}

// CountRecipes returns the number of active recipes.
func CountRecipes(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE active = 1`).Scan(&n)
	return n, err
}

// RecipeSummary is the joined shape used by `meal recipe list`.
type RecipeSummary struct {
	ID              string
	Title           string
	Emoji           string
	Category        string
	ServesRaw       string
	Rating          *int
	TimesCooked     int
	DaysSinceCooked *int
}

// ListRecipeSummaries returns recipes joined with local rating and cook
// history, for `meal recipe list`.
func ListRecipeSummaries(db *sql.DB, opts ListRecipesOpts) ([]RecipeSummary, error) {
	query := `
		SELECT r.id, r.title, r.emoji, r.category, r.serves_raw,
		       rm.rating, v.times_cooked, v.days_since_cooked
		FROM recipes r
		LEFT JOIN recipe_meta rm ON rm.recipe_id = r.id
		LEFT JOIN v_recipe_variety v ON v.recipe_id = r.id
		WHERE 1=1`
	var args []any
	if !opts.IncludeInactive {
		query += ` AND r.active = 1`
	}
	if opts.Category != "" {
		query += ` AND r.category = ?`
		args = append(args, opts.Category)
	}
	if opts.Tag != "" {
		query += ` AND r.tags_json LIKE ?`
		args = append(args, `%"`+opts.Tag+`"%`)
	}
	query += ` ORDER BY r.title`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecipeSummary
	for rows.Next() {
		var s RecipeSummary
		var rating, daysSince sql.NullInt64
		var timesCooked sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Title, &s.Emoji, &s.Category, &s.ServesRaw, &rating, &timesCooked, &daysSince); err != nil {
			return nil, err
		}
		if rating.Valid {
			v := int(rating.Int64)
			s.Rating = &v
		}
		s.TimesCooked = int(timesCooked.Int64)
		if daysSince.Valid {
			v := int(daysSince.Int64)
			s.DaysSinceCooked = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LastSyncedAt returns the most recent recipes.synced_at value, and false if
// no recipe has ever been synced.
func LastSyncedAt(db *sql.DB) (string, bool, error) {
	var v sql.NullString
	if err := db.QueryRow(`SELECT MAX(synced_at) FROM recipes`).Scan(&v); err != nil {
		return "", false, err
	}
	if !v.Valid {
		return "", false, nil
	}
	return v.String, true, nil
}

// GetRecipeMeta fetches local metadata for a recipe, returning a zero-value
// RecipeMeta (not an error) if none has been set yet.
func GetRecipeMeta(db *sql.DB, recipeID string) (RecipeMeta, error) {
	var m RecipeMeta
	m.RecipeID = recipeID
	var costTotal sql.NullFloat64
	var macrosJSON sql.NullString
	var rating sql.NullInt64
	var notes sql.NullString
	var servesOverride sql.NullInt64
	var updatedAt sql.NullString

	err := db.QueryRow(`
		SELECT serves_override, servings_are_meals, cost_total, macros_per_serving_json, rating, notes, updated_at
		FROM recipe_meta WHERE recipe_id = ?
	`, recipeID).Scan(&servesOverride, &m.ServingsAreMeals, &costTotal, &macrosJSON, &rating, &notes, &updatedAt)
	if err == sql.ErrNoRows {
		m.ServingsAreMeals = true
		return m, nil
	}
	if err != nil {
		return RecipeMeta{}, err
	}

	if servesOverride.Valid {
		v := int(servesOverride.Int64)
		m.ServesOverride = &v
	}
	if costTotal.Valid {
		m.CostTotal = &costTotal.Float64
	}
	if rating.Valid {
		v := int(rating.Int64)
		m.Rating = &v
	}
	if notes.Valid {
		m.Notes = &notes.String
	}
	if updatedAt.Valid {
		m.UpdatedAt = updatedAt.String
	}
	if macrosJSON.Valid && macrosJSON.String != "" {
		var mac MacrosPerServing
		if err := json.Unmarshal([]byte(macrosJSON.String), &mac); err != nil {
			return RecipeMeta{}, fmt.Errorf("unmarshaling macros for %s: %w", recipeID, err)
		}
		m.MacrosPerServing = &mac
	}
	return m, nil
}

// UpsertRecipeMeta writes the full RecipeMeta row (read-modify-write pattern:
// callers fetch with GetRecipeMeta, apply their changes, then call this).
func UpsertRecipeMeta(db *sql.DB, m RecipeMeta) error {
	var macrosJSON *string
	if m.MacrosPerServing != nil {
		b, err := json.Marshal(m.MacrosPerServing)
		if err != nil {
			return fmt.Errorf("marshaling macros: %w", err)
		}
		s := string(b)
		macrosJSON = &s
	}
	_, err := db.Exec(`
		INSERT INTO recipe_meta (recipe_id, serves_override, servings_are_meals, cost_total, macros_per_serving_json, rating, notes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(recipe_id) DO UPDATE SET
			serves_override = excluded.serves_override,
			servings_are_meals = excluded.servings_are_meals,
			cost_total = excluded.cost_total,
			macros_per_serving_json = excluded.macros_per_serving_json,
			rating = excluded.rating,
			notes = excluded.notes,
			updated_at = CURRENT_TIMESTAMP
	`, m.RecipeID, m.ServesOverride, m.ServingsAreMeals, m.CostTotal, macrosJSON, m.Rating, m.Notes)
	return err
}
