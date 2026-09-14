package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// CookEvent is the leftover ledger: created by `meal cook`, decremented by
// `meal log --from-leftovers`.
type CookEvent struct {
	ID                int64
	RecipeID          string
	CookedDate        string
	ServingsYielded   float64
	ServingsRemaining float64
	TotalCost         float64
}

// InsertCookEvent creates a new cook event with servings_remaining initialized
// to servings_yielded. Must run inside tx.
func InsertCookEvent(tx *sql.Tx, ce CookEvent) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO cook_events (recipe_id, cooked_date, servings_yielded, servings_remaining, total_cost)
		VALUES (?, ?, ?, ?, ?)
	`, ce.RecipeID, ce.CookedDate, ce.ServingsYielded, ce.ServingsYielded, ce.TotalCost)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ErrInsufficientServings means a decrement would take servings_remaining
// negative - the caller asked to eat/discard more than is actually left.
var ErrInsufficientServings = errors.New("not enough servings remaining")

// DecrementCookEventServings reduces servings_remaining by amount, inside tx.
// Errors (without writing) if amount exceeds what's currently remaining,
// rather than silently clamping to zero - a personal ledger should surface
// that mismatch instead of quietly hiding it.
func DecrementCookEventServings(tx *sql.Tx, id int64, amount float64) error {
	var remaining float64
	if err := tx.QueryRow(`SELECT servings_remaining FROM cook_events WHERE id = ?`, id).Scan(&remaining); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("cook event %d not found", id)
		}
		return err
	}
	if amount > remaining+1e-9 {
		return fmt.Errorf("%w: cook event %d has %.2f servings left, asked to remove %.2f", ErrInsufficientServings, id, remaining, amount)
	}
	_, err := tx.Exec(`UPDATE cook_events SET servings_remaining = servings_remaining - ? WHERE id = ?`, amount, id)
	return err
}

// CookEventDetail joins a cook event with its recipe for display.
type CookEventDetail struct {
	CookEvent
	RecipeTitle   string
	RecipeEmoji   string
	DaysSinceCook int
}

// GetCookEvent fetches one cook event by id.
func GetCookEvent(db *sql.DB, id int64) (CookEvent, error) {
	var ce CookEvent
	err := db.QueryRow(`
		SELECT id, recipe_id, cooked_date, servings_yielded, servings_remaining, total_cost
		FROM cook_events WHERE id = ?
	`, id).Scan(&ce.ID, &ce.RecipeID, &ce.CookedDate, &ce.ServingsYielded, &ce.ServingsRemaining, &ce.TotalCost)
	return ce, err
}

// LatestOpenCookEvent returns the most recently cooked event with servings
// remaining, optionally filtered to a specific recipe.
func LatestOpenCookEvent(db *sql.DB, recipeID string) (CookEvent, error) {
	query := `
		SELECT id, recipe_id, cooked_date, servings_yielded, servings_remaining, total_cost
		FROM cook_events WHERE servings_remaining > 0`
	var args []any
	if recipeID != "" {
		query += ` AND recipe_id = ?`
		args = append(args, recipeID)
	}
	query += ` ORDER BY cooked_date DESC, id DESC LIMIT 1`

	var ce CookEvent
	err := db.QueryRow(query, args...).Scan(&ce.ID, &ce.RecipeID, &ce.CookedDate, &ce.ServingsYielded, &ce.ServingsRemaining, &ce.TotalCost)
	return ce, err
}

// GetCookEventDetail fetches one cook event joined with its recipe, regardless
// of remaining servings (unlike ListOpenCookEvents, this includes fully-eaten
// ones so `meal leftovers show` can review history).
func GetCookEventDetail(db *sql.DB, id int64) (CookEventDetail, error) {
	var d CookEventDetail
	err := db.QueryRow(`
		SELECT ce.id, ce.recipe_id, ce.cooked_date, ce.servings_yielded, ce.servings_remaining, ce.total_cost,
		       r.title, r.emoji,
		       CAST(julianday(date('now','localtime')) - julianday(ce.cooked_date) AS INTEGER)
		FROM cook_events ce
		JOIN recipes r ON r.id = ce.recipe_id
		WHERE ce.id = ?
	`, id).Scan(&d.ID, &d.RecipeID, &d.CookedDate, &d.ServingsYielded, &d.ServingsRemaining,
		&d.TotalCost, &d.RecipeTitle, &d.RecipeEmoji, &d.DaysSinceCook)
	return d, err
}

// VoidCookEventResult summarizes what a void reversed.
type VoidCookEventResult struct {
	CookEventID               int64   `json:"cook_event_id"`
	RecipeID                  string  `json:"recipe_id"`
	ServingsYielded           float64 `json:"servings_yielded"`
	PantryLotsRestored        int     `json:"pantry_lots_restored"`
	ConsumptionEntriesDeleted int     `json:"consumption_entries_deleted"`
}

// VoidCookEvent reverses a cook event entirely: restores every pantry lot it
// decremented (back to the exact lot, quantity, and 'on_hand' status),
// deletes every consumption_log row logged against it (both the immediate
// "cooked" entries and any later "leftover" draws), and removes the cook
// event itself. For undoing a duplicate/erroneous `meal cook` call - never
// for a cook event that genuinely happened but whose leftovers just went to
// waste (use `meal waste log --cook-event-id` for that instead, which
// preserves the historical record of what was actually cooked).
func VoidCookEvent(db *sql.DB, cookEventID int64) (VoidCookEventResult, error) {
	ce, err := GetCookEvent(db, cookEventID)
	if err != nil {
		if err == sql.ErrNoRows {
			return VoidCookEventResult{}, fmt.Errorf("cook event %d not found", cookEventID)
		}
		return VoidCookEventResult{}, err
	}

	ingredients, err := ListCookEventIngredients(db, cookEventID)
	if err != nil {
		return VoidCookEventResult{}, err
	}

	tx, err := db.Begin()
	if err != nil {
		return VoidCookEventResult{}, err
	}
	defer tx.Rollback()

	for _, ci := range ingredients {
		if _, err := tx.Exec(`UPDATE pantry_stock SET quantity = quantity + ?, status = 'on_hand' WHERE id = ?`,
			ci.Quantity, ci.PantryStockID); err != nil {
			return VoidCookEventResult{}, fmt.Errorf("restoring pantry lot %d: %w", ci.PantryStockID, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM cook_event_ingredients WHERE cook_event_id = ?`, cookEventID); err != nil {
		return VoidCookEventResult{}, err
	}

	res, err := tx.Exec(`DELETE FROM consumption_log WHERE cook_event_id = ?`, cookEventID)
	if err != nil {
		return VoidCookEventResult{}, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return VoidCookEventResult{}, err
	}

	if _, err := tx.Exec(`DELETE FROM cook_events WHERE id = ?`, cookEventID); err != nil {
		return VoidCookEventResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return VoidCookEventResult{}, err
	}

	return VoidCookEventResult{
		CookEventID:               cookEventID,
		RecipeID:                  ce.RecipeID,
		ServingsYielded:           ce.ServingsYielded,
		PantryLotsRestored:        len(ingredients),
		ConsumptionEntriesDeleted: int(deleted),
	}, nil
}

// ListOpenCookEvents returns every cook event with servings remaining,
// joined with recipe title/emoji, most recently cooked first.
func ListOpenCookEvents(db *sql.DB) ([]CookEventDetail, error) {
	rows, err := db.Query(`
		SELECT ce.id, ce.recipe_id, ce.cooked_date, ce.servings_yielded, ce.servings_remaining, ce.total_cost,
		       r.title, r.emoji,
		       CAST(julianday(date('now','localtime')) - julianday(ce.cooked_date) AS INTEGER)
		FROM cook_events ce
		JOIN recipes r ON r.id = ce.recipe_id
		WHERE ce.servings_remaining > 0
		ORDER BY ce.cooked_date DESC, ce.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CookEventDetail
	for rows.Next() {
		var d CookEventDetail
		if err := rows.Scan(&d.ID, &d.RecipeID, &d.CookedDate, &d.ServingsYielded, &d.ServingsRemaining,
			&d.TotalCost, &d.RecipeTitle, &d.RecipeEmoji, &d.DaysSinceCook); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
