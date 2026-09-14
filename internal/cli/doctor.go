package cli

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/config"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/usda"
)

// doctorReport is the plain shape both the table and --json renderers consume.
type doctorReport struct {
	DataRoot          string `json:"data_root"`
	DataRootExists    bool   `json:"data_root_exists"`
	DatabaseReachable bool   `json:"database_reachable"`
	MigrationsApplied int    `json:"migrations_applied"`
	PantryCategories  int    `json:"pantry_categories_seeded"`
	USDAKeyConfigured bool   `json:"usda_key_configured"`
	RecipesCached     int    `json:"recipes_cached"`
	RecipesLastSynced string `json:"recipes_last_synced,omitempty"`

	// Data-quality counts. None of these is an error - each is a gap
	// someone can close, and each one silently degrades a number
	// downstream while it stands, which is exactly why they're surfaced
	// here rather than left to be noticed in a total that looks fine.
	ItemsWithoutNutrition int `json:"items_without_nutrition"`
	CachedFoodsNoMacros   int `json:"cached_foods_with_no_macro_data"`
	EntriesWithoutMacros  int `json:"logged_entries_without_macros"`
	RecipesWithoutServes  int `json:"mapped_recipes_without_a_serving_count"`
	UnpricedOnHandLots    int `json:"on_hand_lots_with_no_price"`
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that mealcli's data directory, database, and config are healthy",
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := runDoctor()
			if err != nil {
				return err
			}
			return output.Render(os.Stdout, jsonOutput, report, func() output.Table {
				return output.Table{
					Headers: []string{"CHECK", "VALUE"},
					Rows: [][]string{
						{"data root", report.DataRoot},
						{"data root exists", boolStr(report.DataRootExists)},
						{"database reachable", boolStr(report.DatabaseReachable)},
						{"migrations applied", fmt.Sprintf("%d", report.MigrationsApplied)},
						{"pantry categories seeded", fmt.Sprintf("%d", report.PantryCategories)},
						{"USDA API key configured", boolStr(report.USDAKeyConfigured)},
						{"recipes cached", fmt.Sprintf("%d", report.RecipesCached)},
						{"recipes last synced", emptyDash(report.RecipesLastSynced)},
						{"pantry items without nutrition", fmt.Sprintf("%d", report.ItemsWithoutNutrition)},
						{"cached foods with no macro data", fmt.Sprintf("%d", report.CachedFoodsNoMacros)},
						{"logged entries without macros", fmt.Sprintf("%d", report.EntriesWithoutMacros)},
						{"recipes missing a serving count", fmt.Sprintf("%d", report.RecipesWithoutServes)},
						{"on-hand lots with no price", fmt.Sprintf("%d", report.UnpricedOnHandLots)},
					},
				}
			})
		},
	}
}

func runDoctor() (doctorReport, error) {
	cfg, err := config.Resolve()
	if err != nil {
		return doctorReport{}, err
	}
	report := doctorReport{DataRoot: cfg.DataRoot}

	if _, err := os.Stat(cfg.DataRoot); err == nil {
		report.DataRootExists = true
	}

	key, err := config.USDAAPIKey()
	if err != nil {
		return doctorReport{}, fmt.Errorf("checking USDA key: %w", err)
	}
	report.USDAKeyConfigured = key != ""

	_, db, err := openDB()
	if err != nil {
		// Data dir/db not initialized yet isn't a doctor crash - report it as unreachable.
		return report, nil
	}
	defer db.Close()
	report.DatabaseReachable = true

	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&report.MigrationsApplied); err != nil {
		return doctorReport{}, fmt.Errorf("counting migrations: %w", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pantry_categories`).Scan(&report.PantryCategories); err != nil {
		return doctorReport{}, fmt.Errorf("counting pantry categories: %w", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes`).Scan(&report.RecipesCached); err != nil {
		return doctorReport{}, fmt.Errorf("counting recipes: %w", err)
	}
	var lastSynced sql.NullString
	if err := db.QueryRow(`SELECT MAX(synced_at) FROM recipes`).Scan(&lastSynced); err != nil {
		return doctorReport{}, fmt.Errorf("checking recipe sync freshness: %w", err)
	}
	if lastSynced.Valid {
		report.RecipesLastSynced = lastSynced.String
	}

	// A pantry item with neither a label nor a USDA link can't contribute
	// to any macro total that includes it.
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pantry_items p
		LEFT JOIN item_nutrition n ON n.item_id = p.id
		WHERE p.fdc_id IS NULL AND n.item_id IS NULL
	`).Scan(&report.ItemsWithoutNutrition); err != nil {
		return doctorReport{}, fmt.Errorf("counting items without nutrition: %w", err)
	}

	// A cached food USDA returned with no macro nutrients stores as a row
	// of zeroes, which reads as "this food has none". Counted through the
	// same parser `meal nutrition repair` uses, so the two never disagree
	// about how many there are - a health check that contradicts the
	// command it points at is worse than no health check.
	macroless, err := countMacrolessCachedFoods(db)
	if err != nil {
		return doctorReport{}, err
	}
	report.CachedFoodsNoMacros = macroless

	if err := db.QueryRow(`SELECT COUNT(*) FROM consumption_log WHERE calories IS NULL`).
		Scan(&report.EntriesWithoutMacros); err != nil {
		return doctorReport{}, fmt.Errorf("counting entries without macros: %w", err)
	}

	// A recipe with mapped ingredients but no serving count can total a
	// batch but not divide it, so `meal cook` will refuse it.
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM recipes r
		LEFT JOIN recipe_meta rm ON rm.recipe_id = r.id
		WHERE COALESCE(rm.serves_override, 0) = 0
		  AND TRIM(COALESCE(r.serves_raw, '')) = ''
		  AND EXISTS (SELECT 1 FROM recipe_ingredients ri WHERE ri.recipe_id = r.id AND ri.status = 'mapped')
	`).Scan(&report.RecipesWithoutServes); err != nil {
		return doctorReport{}, fmt.Errorf("counting recipes without a serving count: %w", err)
	}

	// A lot with no unit_cost contributes nothing to any cost figure that
	// includes it, so spend reads lower than it was rather than unknown.
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pantry_stock
		WHERE status = 'on_hand' AND quantity > 0 AND unit_cost IS NULL
	`).Scan(&report.UnpricedOnHandLots); err != nil {
		return doctorReport{}, fmt.Errorf("counting unpriced stock: %w", err)
	}

	return report, nil
}

// countMacrolessCachedFoods counts cached USDA foods that reported no macro
// nutrients at all, as opposed to foods whose macros are genuinely zero
// (salt, baking soda). Only the raw response can tell those apart.
func countMacrolessCachedFoods(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT raw_json FROM nutrition_cache WHERE raw_json != ''`)
	if err != nil {
		return 0, fmt.Errorf("reading nutrition cache: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		food, err := usda.FoodFromRawJSON(raw)
		if err != nil || !food.HasProximates {
			count++
		}
	}
	return count, rows.Err()
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
