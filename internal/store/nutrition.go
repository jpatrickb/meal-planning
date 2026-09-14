package store

import "database/sql"

// NutritionCacheEntry mirrors nutrition_cache: real USDA results only. Crude
// keyword estimates are never cached here (they're regenerated each time,
// since they're not tied to a specific food).
type NutritionCacheEntry struct {
	FDCID           int
	Description     string
	QueryText       string
	CaloriesPer100g float64
	ProteinPer100g  float64
	CarbsPer100g    float64
	FatPer100g      float64
	FiberPer100g    float64
	RawJSON         string
	FetchedAt       string
}

// UpsertNutritionCache stores a USDA result, keyed by fdc_id. QueryText here
// is just "the text that originally produced this" for reference - lookups
// go through nutrition_query_aliases, not this column, so multiple query
// texts can point at the same canonical fdc_id.
func UpsertNutritionCache(db *sql.DB, e NutritionCacheEntry) error {
	_, err := db.Exec(`
		INSERT INTO nutrition_cache (
			fdc_id, description, query_text, calories_per_100g, protein_per_100g,
			carbs_per_100g, fat_per_100g, fiber_per_100g, raw_json, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fdc_id) DO UPDATE SET
			description = excluded.description,
			query_text = excluded.query_text,
			calories_per_100g = excluded.calories_per_100g,
			protein_per_100g = excluded.protein_per_100g,
			carbs_per_100g = excluded.carbs_per_100g,
			fat_per_100g = excluded.fat_per_100g,
			fiber_per_100g = excluded.fiber_per_100g,
			raw_json = excluded.raw_json,
			fetched_at = excluded.fetched_at
	`, e.FDCID, e.Description, e.QueryText, e.CaloriesPer100g, e.ProteinPer100g,
		e.CarbsPer100g, e.FatPer100g, e.FiberPer100g, e.RawJSON, e.FetchedAt)
	return err
}

// GetNutritionByFDCID fetches a canonical cache entry by id.
func GetNutritionByFDCID(db *sql.DB, fdcID int) (NutritionCacheEntry, bool, error) {
	var e NutritionCacheEntry
	err := db.QueryRow(`
		SELECT fdc_id, description, query_text, calories_per_100g, protein_per_100g,
		       carbs_per_100g, fat_per_100g, fiber_per_100g, raw_json, fetched_at
		FROM nutrition_cache WHERE fdc_id = ?
	`, fdcID).Scan(&e.FDCID, &e.Description, &e.QueryText, &e.CaloriesPer100g, &e.ProteinPer100g,
		&e.CarbsPer100g, &e.FatPer100g, &e.FiberPer100g, &e.RawJSON, &e.FetchedAt)
	if err == sql.ErrNoRows {
		return NutritionCacheEntry{}, false, nil
	}
	if err != nil {
		return NutritionCacheEntry{}, false, err
	}
	return e, true, nil
}

// GetNutritionAliasExact looks up a confirmed query-text -> fdc_id mapping.
func GetNutritionAliasExact(db *sql.DB, queryText string) (int, bool, error) {
	var fdcID int
	err := db.QueryRow(`SELECT fdc_id FROM nutrition_query_aliases WHERE query_text = ?`, queryText).Scan(&fdcID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return fdcID, true, nil
}

// UpsertNutritionAlias records (or overwrites) that queryText means fdcID.
// Called both for self-confirmation (the exact text that produced a fresh
// USDA/estimate result) and for an explicit `meal log --reuse-fdc-id`.
func UpsertNutritionAlias(db *sql.DB, queryText string, fdcID int) error {
	_, err := db.Exec(`
		INSERT INTO nutrition_query_aliases (query_text, fdc_id, confirmed_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(query_text) DO UPDATE SET fdc_id = excluded.fdc_id, confirmed_at = CURRENT_TIMESTAMP
	`, queryText, fdcID)
	return err
}

// NutritionCandidate is one existing query text a new query could plausibly
// mean the same food as - for fuzzy-match surfacing, never auto-applied.
type NutritionCandidate struct {
	FDCID       int
	QueryText   string
	Description string
}

// ListNutritionCandidates returns every known (query_text, fdc_id) mapping
// to fuzzy-match a new query against.
func ListNutritionCandidates(db *sql.DB) ([]NutritionCandidate, error) {
	rows, err := db.Query(`
		SELECT nqa.query_text, nqa.fdc_id, nc.description
		FROM nutrition_query_aliases nqa
		JOIN nutrition_cache nc ON nc.fdc_id = nqa.fdc_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NutritionCandidate
	for rows.Next() {
		var c NutritionCandidate
		if err := rows.Scan(&c.QueryText, &c.FDCID, &c.Description); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListNutritionCache returns every cached USDA entry, oldest fdc_id first.
// Used by `meal nutrition repair`, which re-derives macros from each row's
// stored raw_json rather than re-querying USDA.
func ListNutritionCache(db *sql.DB) ([]NutritionCacheEntry, error) {
	rows, err := db.Query(`
		SELECT fdc_id, description, query_text, calories_per_100g, protein_per_100g,
		       carbs_per_100g, fat_per_100g, fiber_per_100g, raw_json, fetched_at
		FROM nutrition_cache ORDER BY fdc_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NutritionCacheEntry
	for rows.Next() {
		var e NutritionCacheEntry
		if err := rows.Scan(&e.FDCID, &e.Description, &e.QueryText, &e.CaloriesPer100g, &e.ProteinPer100g,
			&e.CarbsPer100g, &e.FatPer100g, &e.FiberPer100g, &e.RawJSON, &e.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateNutritionMacros rewrites just the per-100g macro columns of one
// cached entry, leaving description/raw_json/fetched_at alone - the food
// itself hasn't changed, only how it was read.
func UpdateNutritionMacros(tx *sql.Tx, fdcID int, calories, protein, carbs, fat, fiber float64) error {
	_, err := tx.Exec(`
		UPDATE nutrition_cache
		SET calories_per_100g = ?, protein_per_100g = ?, carbs_per_100g = ?, fat_per_100g = ?, fiber_per_100g = ?
		WHERE fdc_id = ?
	`, calories, protein, carbs, fat, fiber, fdcID)
	return err
}
