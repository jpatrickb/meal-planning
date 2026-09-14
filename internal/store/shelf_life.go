package store

import (
	"database/sql"
	"time"
)

// EstimateExpiryDate adds days to acquiredDate (YYYY-MM-DD) and returns the
// result in the same format, for auto-setting pantry_stock.expires_date from
// a typical_shelf_life estimate at purchase time.
func EstimateExpiryDate(acquiredDate string, days int) (string, error) {
	t, err := time.Parse("2006-01-02", acquiredDate)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, days).Format("2006-01-02"), nil
}

// ShelfLife is a typical days-until-expiry estimate for a pantry item.
type ShelfLife struct {
	ItemID int64
	Days   int
	Source string // "patrick_provided" or "estimated"
}

// GetShelfLife looks up a typical shelf-life estimate for an item, if any.
func GetShelfLife(db *sql.DB, itemID int64) (ShelfLife, bool, error) {
	s := ShelfLife{ItemID: itemID}
	err := db.QueryRow(`SELECT days, source FROM typical_shelf_life WHERE item_id = ?`, itemID).Scan(&s.Days, &s.Source)
	if err == sql.ErrNoRows {
		return ShelfLife{}, false, nil
	}
	return s, err == nil, err
}

// SetShelfLife records or updates a typical shelf-life estimate for an item.
func SetShelfLife(db *sql.DB, itemID int64, days int, source string) error {
	_, err := db.Exec(`
		INSERT INTO typical_shelf_life (item_id, days, source, confirmed_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(item_id) DO UPDATE SET days = excluded.days, source = excluded.source, confirmed_at = CURRENT_TIMESTAMP
	`, itemID, days, source)
	return err
}
