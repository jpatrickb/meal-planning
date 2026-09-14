package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/recipesync"
	"github.com/jpatrickb/meal-planning/internal/store"
)

// syncCooldown avoids hitting the site on every accidental re-run; --force bypasses it.
const syncCooldown = 5 * time.Minute

type syncResult struct {
	Skipped        bool   `json:"skipped"`
	SkippedReason  string `json:"skipped_reason,omitempty"`
	RecipesFetched int    `json:"recipes_fetched"`
	SyncedAt       string `json:"synced_at,omitempty"`
}

func newSyncCmd() *cobra.Command {
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Sync data from external sources",
	}
	sync.AddCommand(newSyncRecipesCmd())
	return sync
}

func newSyncRecipesCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "recipes",
		Short: "Fetch and cache recipes.patrickandthea.com/data/recipes.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if !force {
				if lastSynced, ok, err := store.LastSyncedAt(db); err != nil {
					return fmt.Errorf("checking last sync time: %w", err)
				} else if ok {
					t, err := time.Parse(time.RFC3339, lastSynced)
					if err == nil && time.Since(t) < syncCooldown {
						result := syncResult{
							Skipped:       true,
							SkippedReason: fmt.Sprintf("synced %s ago (< %s); use --force to sync anyway", time.Since(t).Round(time.Second), syncCooldown),
							SyncedAt:      lastSynced,
						}
						return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
							return output.Table{
								Headers: []string{"STATUS", "DETAIL"},
								Rows:    [][]string{{"skipped", result.SkippedReason}},
							}
						})
					}
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			res, err := recipesync.Sync(ctx, db)
			if err != nil {
				return fmt.Errorf("syncing recipes: %w", err)
			}

			result := syncResult{RecipesFetched: res.Fetched, SyncedAt: res.Synced.Format(time.RFC3339)}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				return output.Table{
					Headers: []string{"STATUS", "DETAIL"},
					Rows: [][]string{
						{"synced", fmt.Sprintf("%d recipes", result.RecipesFetched)},
						{"synced at", result.SyncedAt},
					},
				}
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "sync even if recently synced")
	return cmd
}
