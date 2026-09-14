package store

import "database/sql"

// PersonCost is one person's total spend within a period.
type PersonCost struct {
	Person string
	Cost   float64
}

// CostAnalytics summarizes consumption_log.cost within a period.
type CostAnalytics struct {
	Since     string
	ByPerson  []PersonCost
	Household float64
}

// GetCostAnalytics sums cost from consumed_date >= since (empty since = all time).
func GetCostAnalytics(db *sql.DB, since string) (CostAnalytics, error) {
	query := `SELECT person, SUM(cost) FROM consumption_log WHERE 1=1`
	var args []any
	if since != "" {
		query += ` AND consumed_date >= ?`
		args = append(args, since)
	}
	query += ` GROUP BY person ORDER BY person`

	rows, err := db.Query(query, args...)
	if err != nil {
		return CostAnalytics{}, err
	}
	defer rows.Close()

	result := CostAnalytics{Since: since}
	for rows.Next() {
		var pc PersonCost
		if err := rows.Scan(&pc.Person, &pc.Cost); err != nil {
			return CostAnalytics{}, err
		}
		result.ByPerson = append(result.ByPerson, pc)
		result.Household += pc.Cost
	}
	return result, rows.Err()
}

// PersonMacros is one person's average daily macros over a period, alongside
// their configured targets (nil fields mean no target set).
type PersonMacros struct {
	Person      string
	DaysLogged  int
	AvgCalories float64
	AvgProtein  float64
	AvgCarbs    float64
	AvgFat      float64
	Target      Target
	HasTarget   bool
}

// MacroAnalytics summarizes per-day macro totals, averaged, within a period.
type MacroAnalytics struct {
	Since    string
	ByPerson []PersonMacros
}

// GetMacroAnalytics averages v_daily_totals rows within the period, joined
// with each person's targets.
func GetMacroAnalytics(db *sql.DB, since string) (MacroAnalytics, error) {
	query := `
		SELECT person, COUNT(*), AVG(total_calories), AVG(total_protein), AVG(total_carbs), AVG(total_fat)
		FROM v_daily_totals WHERE 1=1`
	var args []any
	if since != "" {
		query += ` AND consumed_date >= ?`
		args = append(args, since)
	}
	query += ` GROUP BY person ORDER BY person`

	rows, err := db.Query(query, args...)
	if err != nil {
		return MacroAnalytics{}, err
	}
	// Collect fully and close before any nested query (GetTarget below) runs -
	// holding this cursor open while issuing another query on the same *sql.DB
	// is a deadlock risk regardless of how large the connection pool is.
	var byPerson []PersonMacros
	for rows.Next() {
		var pm PersonMacros
		if err := rows.Scan(&pm.Person, &pm.DaysLogged, &pm.AvgCalories, &pm.AvgProtein, &pm.AvgCarbs, &pm.AvgFat); err != nil {
			rows.Close()
			return MacroAnalytics{}, err
		}
		byPerson = append(byPerson, pm)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MacroAnalytics{}, err
	}
	rows.Close()

	result := MacroAnalytics{Since: since}
	for _, pm := range byPerson {
		target, ok, err := GetTarget(db, pm.Person)
		if err != nil {
			return MacroAnalytics{}, err
		}
		pm.Target, pm.HasTarget = target, ok
		result.ByPerson = append(result.ByPerson, pm)
	}
	return result, nil
}

// ReasonWaste is one waste reason's occurrence count and cost within a period.
type ReasonWaste struct {
	Reason      string
	Occurrences int
	TotalCost   float64
}

// WasteAnalytics summarizes waste_log within a period.
type WasteAnalytics struct {
	Since     string
	ByReason  []ReasonWaste
	TotalCost float64
}

// GetWasteAnalytics groups waste_log by reason within the period.
func GetWasteAnalytics(db *sql.DB, since string) (WasteAnalytics, error) {
	query := `SELECT reason, COUNT(*), SUM(estimated_cost) FROM waste_log WHERE 1=1`
	var args []any
	if since != "" {
		query += ` AND wasted_date >= ?`
		args = append(args, since)
	}
	query += ` GROUP BY reason ORDER BY SUM(estimated_cost) DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return WasteAnalytics{}, err
	}
	defer rows.Close()

	result := WasteAnalytics{Since: since}
	for rows.Next() {
		var rw ReasonWaste
		var cost sql.NullFloat64
		if err := rows.Scan(&rw.Reason, &rw.Occurrences, &cost); err != nil {
			return WasteAnalytics{}, err
		}
		rw.TotalCost = cost.Float64
		result.ByReason = append(result.ByReason, rw)
		result.TotalCost += rw.TotalCost
	}
	return result, rows.Err()
}

// VarietyEntry is one recipe's cook history, from v_recipe_variety.
type VarietyEntry struct {
	RecipeID        string
	Title           string
	Rating          *int
	TimesCooked     int
	DaysSinceCooked *int
}

// GetVarietySummary returns every active recipe's cook history, ordered so
// never-cooked and long-unused recipes surface first (days_since_cooked NULL
// sorts as "infinitely stale" via the CASE, ahead of any real number).
func GetVarietySummary(db *sql.DB, limit int) ([]VarietyEntry, error) {
	query := `
		SELECT recipe_id, title, rating, times_cooked, days_since_cooked
		FROM v_recipe_variety
		ORDER BY CASE WHEN days_since_cooked IS NULL THEN 1 ELSE 0 END DESC, days_since_cooked DESC, title`
	if limit > 0 {
		query += ` LIMIT ?`
	}

	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = db.Query(query, limit)
	} else {
		rows, err = db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VarietyEntry
	for rows.Next() {
		var v VarietyEntry
		if err := rows.Scan(&v.RecipeID, &v.Title, &v.Rating, &v.TimesCooked, &v.DaysSinceCooked); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
