package store

import "database/sql"

// ItemAlias maps a normalized receipt-line string (or UPC) to a canonical
// pantry item. Confirmed=false means it's a fuzzy-match candidate awaiting
// human confirmation, not yet trusted for automatic matching.
type ItemAlias struct {
	ID                int64
	RawTextNormalized string
	UPC               *string
	ItemID            int64
	Confidence        *float64
	Confirmed         bool
	TimesMatched      int
	LastMatchedAt     *string
}

func scanAlias(row interface{ Scan(dest ...any) error }) (ItemAlias, error) {
	var a ItemAlias
	err := row.Scan(&a.ID, &a.RawTextNormalized, &a.UPC, &a.ItemID, &a.Confidence, &a.Confirmed, &a.TimesMatched, &a.LastMatchedAt)
	return a, err
}

const aliasColumns = `id, raw_text_normalized, upc, item_id, confidence, confirmed, times_matched, last_matched_at`

// GetAliasByUPC looks up a confirmed alias by UPC.
func GetAliasByUPC(db *sql.DB, upc string) (ItemAlias, bool, error) {
	a, err := scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM item_aliases WHERE upc = ? AND confirmed = 1`, upc))
	if err == sql.ErrNoRows {
		return ItemAlias{}, false, nil
	}
	return a, err == nil, err
}

// GetAliasByNormalizedText looks up a confirmed alias by exact normalized text.
func GetAliasByNormalizedText(db *sql.DB, text string) (ItemAlias, bool, error) {
	a, err := scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM item_aliases WHERE raw_text_normalized = ? AND confirmed = 1`, text))
	if err == sql.ErrNoRows {
		return ItemAlias{}, false, nil
	}
	return a, err == nil, err
}

// GetAlias fetches an alias by id, regardless of confirmation state.
func GetAlias(db *sql.DB, id int64) (ItemAlias, error) {
	return scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM item_aliases WHERE id = ?`, id))
}

// GetPendingAliasByNormalizedText looks up an unconfirmed alias by exact
// normalized text, regardless of confirmation state (used by `alias
// confirm --raw` to find a fuzzy candidate to promote).
func GetPendingAliasByNormalizedText(db *sql.DB, text string) (ItemAlias, bool, error) {
	a, err := scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM item_aliases WHERE raw_text_normalized = ?`, text))
	if err == sql.ErrNoRows {
		return ItemAlias{}, false, nil
	}
	return a, err == nil, err
}

// BumpAliasUsage increments times_matched and stamps last_matched_at on an
// exact/UPC hit, inside tx.
func BumpAliasUsage(tx *sql.Tx, aliasID int64) error {
	_, err := tx.Exec(`UPDATE item_aliases SET times_matched = times_matched + 1, last_matched_at = CURRENT_TIMESTAMP WHERE id = ?`, aliasID)
	return err
}

// UpsertConfirmedAlias creates or updates an alias as confirmed (confidence
// 1.0), keyed by normalized text. This is the "self-reinforcement" step: any
// manual match (shop add-item --item-id/--create-new, or alias confirm)
// writes here so the identical raw text resolves instantly next time.
func UpsertConfirmedAlias(db *sql.DB, normalizedText string, upc *string, itemID int64) (int64, error) {
	_, err := db.Exec(`
		INSERT INTO item_aliases (raw_text_normalized, upc, item_id, confidence, confirmed, times_matched, last_matched_at)
		VALUES (?, ?, ?, 1.0, 1, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(raw_text_normalized) DO UPDATE SET
			item_id = excluded.item_id,
			upc = COALESCE(excluded.upc, item_aliases.upc),
			confidence = 1.0,
			confirmed = 1,
			times_matched = item_aliases.times_matched + 1,
			last_matched_at = CURRENT_TIMESTAMP
	`, normalizedText, upc, itemID)
	if err != nil {
		return 0, err
	}
	a, _, err := GetAliasByNormalizedText(db, normalizedText)
	return a.ID, err
}

// CreatePendingAlias records a fuzzy-match candidate awaiting confirmation.
// If a pending alias already exists for this exact text, its guess is
// refreshed rather than duplicated.
func CreatePendingAlias(db *sql.DB, normalizedText string, itemID int64, confidence float64) (int64, error) {
	_, err := db.Exec(`
		INSERT INTO item_aliases (raw_text_normalized, item_id, confidence, confirmed, times_matched, last_matched_at)
		VALUES (?, ?, ?, 0, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(raw_text_normalized) DO UPDATE SET
			item_id = excluded.item_id,
			confidence = excluded.confidence,
			times_matched = item_aliases.times_matched + 1,
			last_matched_at = CURRENT_TIMESTAMP
		WHERE item_aliases.confirmed = 0
	`, normalizedText, itemID, confidence)
	if err != nil {
		return 0, err
	}
	a, ok, err := GetPendingAliasByNormalizedText(db, normalizedText)
	if err != nil {
		return 0, err
	}
	if !ok {
		// A confirmed alias already existed for this text (the WHERE guard
		// skipped the update) - nothing to do, exact matching already covers it.
		confirmed, _, err := GetAliasByNormalizedText(db, normalizedText)
		return confirmed.ID, err
	}
	return a.ID, nil
}

// ConfirmAlias promotes an existing alias row (by id) to confirmed, setting
// item_id and resetting confidence to 1.0.
func ConfirmAlias(db *sql.DB, aliasID, itemID int64) error {
	res, err := db.Exec(`UPDATE item_aliases SET item_id = ?, confirmed = 1, confidence = 1.0 WHERE id = ?`, itemID, aliasID)
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

// ListAliases returns aliases joined with their item's name, optionally
// filtered to only unconfirmed (pending) ones.
func ListAliases(db *sql.DB, unconfirmedOnly bool) ([]ItemAliasDetail, error) {
	query := `
		SELECT ia.id, ia.raw_text_normalized, ia.upc, ia.item_id, pi.name, ia.confidence, ia.confirmed,
		       ia.times_matched, ia.last_matched_at
		FROM item_aliases ia
		JOIN pantry_items pi ON pi.id = ia.item_id`
	if unconfirmedOnly {
		query += ` WHERE ia.confirmed = 0`
	}
	query += ` ORDER BY ia.last_matched_at DESC`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ItemAliasDetail
	for rows.Next() {
		var d ItemAliasDetail
		if err := rows.Scan(&d.ID, &d.RawTextNormalized, &d.UPC, &d.ItemID, &d.ItemName, &d.Confidence,
			&d.Confirmed, &d.TimesMatched, &d.LastMatchedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ItemAliasDetail is an alias joined with its item's name, for display.
type ItemAliasDetail struct {
	ID                int64
	RawTextNormalized string
	UPC               *string
	ItemID            int64
	ItemName          string
	Confidence        *float64
	Confirmed         bool
	TimesMatched      int
	LastMatchedAt     *string
}

// MatchCandidate is one string a fuzzy match can be scored against: either
// an existing item's canonical name, or a previously-seen alias text.
type MatchCandidate struct {
	ItemID int64
	Text   string
}

// ListMatchCandidates returns every item name plus every alias text, for
// fuzzy matching a new receipt line against.
func ListMatchCandidates(db *sql.DB) ([]MatchCandidate, error) {
	rows, err := db.Query(`
		SELECT id AS item_id, name AS text FROM pantry_items
		UNION
		SELECT item_id, raw_text_normalized AS text FROM item_aliases
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MatchCandidate
	for rows.Next() {
		var c MatchCandidate
		if err := rows.Scan(&c.ItemID, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
