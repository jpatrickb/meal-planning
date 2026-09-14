package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

// backfilledEntry is one consumption row that can now be given macros.
type backfilledEntry struct {
	ID         int64
	Date       string
	Person     string
	RecipeID   string
	Servings   float64
	Macros     store.MacrosPerServing
	SkipReason string
}

// newRecipeBackfillMacrosCmd fills in macros on recipe-based consumption
// entries logged before their recipe could produce a number.
//
// Only entries that have no macros at all are touched: an entry someone set
// deliberately (or that came from a manual recipe_meta value) is left alone,
// since the point is to close gaps, not to overwrite decisions. Entries
// whose recipe still can't resolve macros are reported with the reason
// rather than silently skipped.
func newRecipeBackfillMacrosCmd() *cobra.Command {
	var dryRun, recompute bool
	cmd := &cobra.Command{
		Use:   "backfill-macros",
		Short: "Fill in macros on already-logged cooked and leftover meals whose recipe can now compute them",
		Long: "Fill in macros on already-logged cooked and leftover meals whose recipe can now compute them.\n\n" +
			"Entries tied to a cook event are computed from what that cook actually consumed, divided by\n" +
			"the servings it actually yielded, so every serving of one batch agrees with every other.",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			entries, err := store.ListConsumptionEntries(db, store.ConsumptionListOpts{})
			if err != nil {
				return fmt.Errorf("listing consumption log: %w", err)
			}

			// One resolution per cook event (or per recipe, for an entry not
			// tied to one), so every serving of a batch agrees.
			resolved := map[string]domain.RecipeMacroResolution{}
			var planned []backfilledEntry
			for _, e := range entries {
				if e.RecipeID == nil {
					continue
				}
				// Only fill gaps, unless asked to recompute - and even then,
				// never over a hand-set value. A previously computed entry
				// is marked "usda" or "estimated"; "local_meta" means a
				// person decided it.
				computedBefore := e.MacroSource != nil && (*e.MacroSource == "usda" || *e.MacroSource == "estimated")
				if e.Calories != nil && !(recompute && computedBefore) {
					continue
				}

				key := *e.RecipeID
				if e.CookEventID != nil {
					key = fmt.Sprintf("event:%d", *e.CookEventID)
				}
				res, ok := resolved[key]
				if !ok {
					meta, err := store.GetRecipeMeta(db, *e.RecipeID)
					if err != nil {
						return fmt.Errorf("loading metadata for %s: %w", *e.RecipeID, err)
					}
					if e.CookEventID != nil {
						ce, err := store.GetCookEvent(db, *e.CookEventID)
						if err != nil {
							return fmt.Errorf("loading cook event %d: %w", *e.CookEventID, err)
						}
						res, err = resolveCookEventMacros(db, ce, meta)
						if err != nil {
							return err
						}
					} else {
						recipe, err := store.GetRecipe(db, *e.RecipeID)
						if err != nil {
							return fmt.Errorf("loading recipe %s: %w", *e.RecipeID, err)
						}
						servings, _ := domain.ResolveServings(nil, meta.ServesOverride, recipe.ServesRaw, *e.RecipeID)
						res, err = domain.ResolveRecipeMacros(db, *e.RecipeID, meta, float64(servings))
						if err != nil {
							return fmt.Errorf("resolving macros for %s: %w", *e.RecipeID, err)
						}
					}
					resolved[key] = res
				}

				row := backfilledEntry{ID: e.ID, Date: e.ConsumedDate, Person: e.Person, RecipeID: key, Servings: e.Servings}
				if res.Source == "unknown" {
					row.SkipReason = res.Reason
					planned = append(planned, row)
					continue
				}
				m := res.PerServing
				row.Macros = store.MacrosPerServing{
					Calories: m.Calories * e.Servings, Protein: m.Protein * e.Servings,
					Carbs: m.Carbs * e.Servings, Fat: m.Fat * e.Servings, Fiber: m.Fiber * e.Servings,
				}
				planned = append(planned, row)
			}

			if !dryRun {
				tx, err := db.Begin()
				if err != nil {
					return fmt.Errorf("starting transaction: %w", err)
				}
				defer tx.Rollback()
				for _, p := range planned {
					if p.SkipReason != "" {
						continue
					}
					if err := store.UpdateConsumptionMacros(tx, p.ID, p.Macros.Calories, p.Macros.Protein,
						p.Macros.Carbs, p.Macros.Fat, p.Macros.Fiber); err != nil {
						return fmt.Errorf("updating entry %d: %w", p.ID, err)
					}
					source := "usda"
					switch resolved[p.RecipeID].Source {
					case "manual":
						source = "local_meta"
					case "computed_estimated":
						source = "estimated"
					}
					if err := store.SetConsumptionMacroSource(tx, p.ID, source); err != nil {
						return fmt.Errorf("stamping macro source on entry %d: %w", p.ID, err)
					}
				}
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("committing: %w", err)
				}
			}

			var filled, skipped []backfilledEntry
			for _, p := range planned {
				if p.SkipReason == "" {
					filled = append(filled, p)
				} else {
					skipped = append(skipped, p)
				}
			}

			if jsonOutput {
				return output.Render(os.Stdout, true, map[string]any{
					"dry_run": dryRun, "filled": filled, "skipped": skipped,
				}, nil)
			}

			verb := "Filled in"
			if dryRun {
				verb = "Would fill in"
			}
			if len(filled) > 0 {
				rows := make([][]string, 0, len(filled))
				for _, p := range filled {
					rows = append(rows, []string{
						fmt.Sprintf("%d", p.ID), p.Date, p.Person, truncate(p.RecipeID, 32),
						fmt.Sprintf("%.2f", p.Servings), fmt.Sprintf("%.0f", p.Macros.Calories),
						fmt.Sprintf("%.1f", p.Macros.Protein),
					})
				}
				fmt.Printf("%s macros on %d logged entr(ies):\n\n", verb, len(filled))
				if err := output.Render(os.Stdout, false, nil, func() output.Table {
					return output.Table{Headers: []string{"LOG ID", "DATE", "PERSON", "RECIPE", "SERVINGS", "CALORIES", "PROTEIN"}, Rows: rows}
				}); err != nil {
					return err
				}
			} else {
				fmt.Println("No logged entries were waiting on macros their recipe can now compute.")
			}

			if len(skipped) > 0 {
				byRecipe := map[string]string{}
				for _, p := range skipped {
					byRecipe[p.RecipeID] = p.SkipReason
				}
				fmt.Printf("\n%d entr(ies) still can't be filled in:\n", len(skipped))
				for recipe, reason := range byRecipe {
					fmt.Printf("  %s: %s\n", recipe, reason)
				}
			}
			if dryRun {
				fmt.Println("\nDry run: nothing was written.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	cmd.Flags().BoolVar(&recompute, "recompute", false, "also redo entries whose macros were computed before, for when the underlying data has changed (never touches a hand-set value)")
	return cmd
}
