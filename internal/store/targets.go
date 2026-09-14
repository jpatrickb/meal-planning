package store

import "database/sql"

// Target is a person's daily macro targets, used by `meal analytics macros`
// to show actual-vs-target. Any field may be unset (nil).
type Target struct {
	Person  string
	Kcal    *float64
	Protein *float64
	Carbs   *float64
	Fat     *float64
}

// UpsertTarget inserts or replaces a person's targets.
func UpsertTarget(db *sql.DB, t Target) error {
	_, err := db.Exec(`
		INSERT INTO targets (person, kcal, protein, carbs, fat) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(person) DO UPDATE SET
			kcal = excluded.kcal, protein = excluded.protein, carbs = excluded.carbs, fat = excluded.fat
	`, t.Person, t.Kcal, t.Protein, t.Carbs, t.Fat)
	return err
}

// GetTarget fetches one person's targets. Returns ok=false (not an error) if none set.
func GetTarget(db *sql.DB, person string) (Target, bool, error) {
	var t Target
	t.Person = person
	err := db.QueryRow(`SELECT kcal, protein, carbs, fat FROM targets WHERE person = ?`, person).
		Scan(&t.Kcal, &t.Protein, &t.Carbs, &t.Fat)
	if err == sql.ErrNoRows {
		return Target{}, false, nil
	}
	if err != nil {
		return Target{}, false, err
	}
	return t, true, nil
}

// ListTargets returns every configured target, ordered by person.
func ListTargets(db *sql.DB) ([]Target, error) {
	rows, err := db.Query(`SELECT person, kcal, protein, carbs, fat FROM targets ORDER BY person`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.Person, &t.Kcal, &t.Protein, &t.Carbs, &t.Fat); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
