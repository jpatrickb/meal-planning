package store

import (
	"database/sql"
	"fmt"
)

// ConsumptionEntry is one row of consumption_log. A shared meal produces two
// entries (one per person) linked by EventGroupID, rather than a single row
// with person='both' - keeps every aggregate a plain GROUP BY person.
type ConsumptionEntry struct {
	ID           int64
	ConsumedDate string
	EventGroupID *int64
	Person       string
	MealSlot     *string
	Source       string
	RecipeID     *string
	CookEventID  *int64
	FreeTextFood *string
	Servings     float64
	Cost         float64
	Calories     *float64
	Protein      *float64
	Carbs        *float64
	Fat          *float64
	Fiber        *float64
	MacroSource  *string
}

// InsertConsumptionEntry inserts one row, inside tx.
func InsertConsumptionEntry(tx *sql.Tx, e ConsumptionEntry) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO consumption_log (
			consumed_date, event_group_id, person, meal_slot, source, recipe_id, cook_event_id,
			free_text_food, servings, cost, calories, protein, carbs, fat, fiber, macro_source
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ConsumedDate, e.EventGroupID, e.Person, e.MealSlot, e.Source, e.RecipeID, e.CookEventID,
		e.FreeTextFood, e.Servings, e.Cost, e.Calories, e.Protein, e.Carbs, e.Fat, e.Fiber, e.MacroSource)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetConsumptionEventGroup stamps event_group_id on a set of rows, inside tx.
// Used to link multiple consumption_log rows written by one `meal cook
// --eaten-now` or `meal log` call, once the group's id (the first row's own
// id) is known.
func SetConsumptionEventGroup(tx *sql.Tx, ids []int64, groupID int64) error {
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE consumption_log SET event_group_id = ? WHERE id = ?`, groupID, id); err != nil {
			return err
		}
	}
	return nil
}

// ConsumptionListOpts filters ListConsumptionEntries.
type ConsumptionListOpts struct {
	Person   string
	DateFrom string
	DateTo   string
	Limit    int
}

// ListConsumptionEntries returns log rows matching opts, most recent first.
func ListConsumptionEntries(db *sql.DB, opts ConsumptionListOpts) ([]ConsumptionEntry, error) {
	query := `
		SELECT id, consumed_date, event_group_id, person, meal_slot, source, recipe_id, cook_event_id,
		       free_text_food, servings, cost, calories, protein, carbs, fat, fiber, macro_source
		FROM consumption_log WHERE 1=1`
	var args []any
	if opts.Person != "" {
		query += ` AND person = ?`
		args = append(args, opts.Person)
	}
	if opts.DateFrom != "" {
		query += ` AND consumed_date >= ?`
		args = append(args, opts.DateFrom)
	}
	if opts.DateTo != "" {
		query += ` AND consumed_date <= ?`
		args = append(args, opts.DateTo)
	}
	query += ` ORDER BY consumed_date DESC, id DESC`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConsumptionEntry
	for rows.Next() {
		var e ConsumptionEntry
		if err := rows.Scan(&e.ID, &e.ConsumedDate, &e.EventGroupID, &e.Person, &e.MealSlot, &e.Source,
			&e.RecipeID, &e.CookEventID, &e.FreeTextFood, &e.Servings, &e.Cost,
			&e.Calories, &e.Protein, &e.Carbs, &e.Fat, &e.Fiber, &e.MacroSource); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VoidedLogEntry describes a consumption row removed by VoidLogEntry, and
// the pantry stock the removal put back.
type VoidedLogEntry struct {
	ID           int64
	ConsumedDate string
	Person       string
	MealSlot     string
	Food         string
	Cost         float64
	Source       string
	RestoredLots []RestoredLot
	// CookEventID and ServingsReturned are set when the voided entry drew on
	// a cook event's leftovers, so the caller can report what went back.
	CookEventID      *int64
	ServingsReturned float64
}

// RestoredLot is one lot a void gave stock back to.
type RestoredLot struct {
	ItemID   int64
	ItemName string
	StockID  int64
	Quantity float64
	Unit     string
}

// VoidLogEntry deletes one consumption_log row and puts back any pantry
// stock it drew from, returning what was restored.
//
// Each depletion goes back onto the exact lot it came from, so the lot keeps
// its acquired date, expiration and real purchase price - none of which a
// replacement lot could reproduce without someone guessing. Entries logged
// before consumption_depletions existed have nothing recorded to give back;
// RestoredLots is empty for those and the caller should say so rather than
// imply the pantry is now correct.
func VoidLogEntry(db *sql.DB, id int64) (VoidedLogEntry, error) {
	var v VoidedLogEntry
	err := db.QueryRow(`
		SELECT id, consumed_date, person, COALESCE(meal_slot,''),
		       COALESCE(free_text_food, COALESCE(recipe_id,'(unnamed)')), cost, source,
		       cook_event_id, servings
		FROM consumption_log WHERE id = ?`, id).
		Scan(&v.ID, &v.ConsumedDate, &v.Person, &v.MealSlot, &v.Food, &v.Cost, &v.Source,
			&v.CookEventID, &v.ServingsReturned)
	if err == sql.ErrNoRows {
		return v, fmt.Errorf("no consumption log entry with id %d", id)
	}
	if err != nil {
		return v, err
	}

	depletions, err := ListConsumptionDepletions(db, id)
	if err != nil {
		return v, fmt.Errorf("loading what entry %d depleted: %w", id, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()

	for _, d := range depletions {
		if err := RestoreDepletedLot(tx, d.PantryStockID, d.Quantity); err != nil {
			return v, fmt.Errorf("restoring stock to lot %d: %w", d.PantryStockID, err)
		}
		name := fmt.Sprintf("item %d", d.ItemID)
		if err := tx.QueryRow(`SELECT name FROM pantry_items WHERE id = ?`, d.ItemID).Scan(&name); err != nil && err != sql.ErrNoRows {
			return v, err
		}
		v.RestoredLots = append(v.RestoredLots, RestoredLot{
			ItemID: d.ItemID, ItemName: name, StockID: d.PantryStockID, Quantity: d.Quantity, Unit: d.Unit,
		})
	}

	// A leftover draw also has to give its servings back to the cook event.
	// Without this the ledger silently loses them: the row is gone, but
	// servings_remaining still reflects a helping nobody ate.
	if v.CookEventID != nil && v.ServingsReturned != 0 {
		if _, err := tx.Exec(
			`UPDATE cook_events SET servings_remaining = servings_remaining + ? WHERE id = ?`,
			v.ServingsReturned, *v.CookEventID); err != nil {
			return v, fmt.Errorf("returning %.2f servings to cook event %d: %w",
				v.ServingsReturned, *v.CookEventID, err)
		}
	} else {
		v.ServingsReturned = 0
	}

	res, err := tx.Exec(`DELETE FROM consumption_log WHERE id = ?`, id)
	if err != nil {
		return v, fmt.Errorf("deleting consumption log entry %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return v, fmt.Errorf("consumption log entry %d was not deleted", id)
	}
	// The depletion rows go with it via ON DELETE CASCADE, which is right:
	// they describe an entry that no longer exists, and the stock they
	// described is back where it came from.
	if err := tx.Commit(); err != nil {
		return v, fmt.Errorf("committing: %w", err)
	}
	return v, nil
}

// UpdateConsumptionMacros rewrites the macro columns of one logged entry,
// inside tx. Only `meal nutrition repair` uses this: an entry's macros are
// otherwise fixed at log time, and a correction that isn't a re-read of the
// same food should go through void-and-relog instead.
func UpdateConsumptionMacros(tx *sql.Tx, id int64, calories, protein, carbs, fat, fiber float64) error {
	res, err := tx.Exec(`
		UPDATE consumption_log
		SET calories = ?, protein = ?, carbs = ?, fat = ?, fiber = ?
		WHERE id = ?
	`, calories, protein, carbs, fat, fiber, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("consumption log entry %d not found", id)
	}
	return nil
}

// SetConsumptionMacroSource stamps the provenance of an entry's macros,
// inside tx. Paired with UpdateConsumptionMacros by `meal log set-macros`;
// `meal nutrition repair` deliberately leaves it alone, since a re-read of
// the same USDA food doesn't change where the numbers came from.
func SetConsumptionMacroSource(tx *sql.Tx, id int64, macroSource string) error {
	_, err := tx.Exec(`UPDATE consumption_log SET macro_source = ? WHERE id = ?`, macroSource, id)
	return err
}
