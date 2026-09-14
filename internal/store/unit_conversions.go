package store

import "database/sql"

// UnitConversion is a real conversion factor between two units, either
// universal (ItemID nil, e.g. cup -> ml) or specific to one pantry item
// (density/size-dependent, e.g. a cup of flour vs. a cup of milk, or one
// carrot's average weight). quantity(to_unit) = quantity(from_unit) * factor.
// See docs/requirements-ingredient-costing.md §6.
type UnitConversion struct {
	ID          int64
	ItemID      *int64
	FromUnit    string
	ToUnit      string
	Factor      float64
	ConfirmedAt string
}

// GetUnitConversion looks up a conversion for one item, falling back to a
// universal (item_id NULL) conversion if no item-specific one exists.
func GetUnitConversion(db *sql.DB, itemID *int64, fromUnit, toUnit string) (UnitConversion, bool, error) {
	if itemID != nil {
		uc, ok, err := getUnitConversion(db, itemID, fromUnit, toUnit)
		if err != nil || ok {
			return uc, ok, err
		}
	}
	return getUnitConversion(db, nil, fromUnit, toUnit)
}

func getUnitConversion(db *sql.DB, itemID *int64, fromUnit, toUnit string) (UnitConversion, bool, error) {
	var row *sql.Row
	if itemID == nil {
		row = db.QueryRow(`SELECT id, item_id, from_unit, to_unit, factor, confirmed_at FROM unit_conversions WHERE item_id IS NULL AND from_unit = ? AND to_unit = ?`, fromUnit, toUnit)
	} else {
		row = db.QueryRow(`SELECT id, item_id, from_unit, to_unit, factor, confirmed_at FROM unit_conversions WHERE item_id = ? AND from_unit = ? AND to_unit = ?`, *itemID, fromUnit, toUnit)
	}
	var uc UnitConversion
	err := row.Scan(&uc.ID, &uc.ItemID, &uc.FromUnit, &uc.ToUnit, &uc.Factor, &uc.ConfirmedAt)
	if err == sql.ErrNoRows {
		return UnitConversion{}, false, nil
	}
	return uc, err == nil, err
}

// CreateUnitConversion records a new conversion factor, or updates the
// factor if one already exists for this (item_id, from_unit, to_unit) --
// checked in application code since SQLite's UNIQUE index treats NULL
// item_id values as always-distinct, so it can't enforce this case itself.
func CreateUnitConversion(db *sql.DB, itemID *int64, fromUnit, toUnit string, factor float64) (int64, error) {
	existing, ok, err := getUnitConversion(db, itemID, fromUnit, toUnit)
	if err != nil {
		return 0, err
	}
	if ok {
		_, err := db.Exec(`UPDATE unit_conversions SET factor = ?, confirmed_at = CURRENT_TIMESTAMP WHERE id = ?`, factor, existing.ID)
		return existing.ID, err
	}
	res, err := db.Exec(`INSERT INTO unit_conversions (item_id, from_unit, to_unit, factor, confirmed_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`, itemID, fromUnit, toUnit, factor)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UnitConverter converts a quantity between two units, reporting false when
// there's no recorded basis for doing so. Passed in from the domain layer,
// which owns the conversion rules, so the store can compare quantities
// across units without importing it.
type UnitConverter func(qty float64, fromUnit, toUnit string) (float64, bool)

// ListUnitConversions returns the conversion facts applicable to one item:
// its own first, then the universal ones. Item-specific facts come first so
// a caller trying them in order gets the more specific answer.
func ListUnitConversions(db *sql.DB, itemID *int64) ([]UnitConversion, error) {
	var rows *sql.Rows
	var err error
	if itemID != nil {
		rows, err = db.Query(`
			SELECT id, item_id, from_unit, to_unit, factor, confirmed_at FROM unit_conversions
			WHERE item_id = ? OR item_id IS NULL
			ORDER BY item_id IS NULL, id
		`, *itemID)
	} else {
		rows, err = db.Query(`
			SELECT id, item_id, from_unit, to_unit, factor, confirmed_at FROM unit_conversions
			WHERE item_id IS NULL ORDER BY id
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UnitConversion
	for rows.Next() {
		var uc UnitConversion
		if err := rows.Scan(&uc.ID, &uc.ItemID, &uc.FromUnit, &uc.ToUnit, &uc.Factor, &uc.ConfirmedAt); err != nil {
			return nil, err
		}
		out = append(out, uc)
	}
	return out, rows.Err()
}

// SetUnitConversionSource records whether a factor was measured/stated or is
// a standard reference estimate. Separate from CreateUnitConversion so the
// existing call sites keep their signature.
func SetUnitConversionSource(db *sql.DB, id int64, source string) error {
	_, err := db.Exec(`UPDATE unit_conversions SET source = ? WHERE id = ?`, source, id)
	return err
}
