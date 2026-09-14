package cli

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/config"
	"github.com/jpatrickb/meal-planning/internal/store"
)

var jsonOutput bool

// Execute builds the root command tree and runs it. This is the single entry
// point called from cmd/mealcli/main.go.
func Execute() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "meal",
		Short:         "Personal meal planning: pantry, recipes, consumption, and analytics",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output machine-readable JSON instead of a table")

	root.AddCommand(newInitCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newRecipeCmd())
	root.AddCommand(newCookCmd())
	root.AddCommand(newLogCmd())
	root.AddCommand(newLeftoversCmd())
	root.AddCommand(newTargetCmd())
	root.AddCommand(newAnalyticsCmd())
	root.AddCommand(newPantryCmd())
	root.AddCommand(newShopCmd())
	root.AddCommand(newAliasCmd())
	root.AddCommand(newWasteCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newShoppingListCmd())
	root.AddCommand(newNutritionCmd())
	root.AddCommand(newPublishCmd())

	return root
}

// openDB resolves config and opens (migrating as needed) the database for a
// command that needs it. Commands that don't need the DB (e.g. `meal init`
// before it exists) should not call this.
func openDB() (*config.Config, *sql.DB, error) {
	cfg, err := config.Resolve()
	if err != nil {
		return nil, nil, fmt.Errorf("resolving config: %w", err)
	}
	db, err := store.Open(cfg.DataRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("opening database: %w", err)
	}
	return cfg, db, nil
}
