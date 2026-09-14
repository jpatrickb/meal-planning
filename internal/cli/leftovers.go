package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newLeftoversCmd() *cobra.Command {
	leftovers := &cobra.Command{
		Use:   "leftovers",
		Short: "View the open leftover ledger",
	}
	leftovers.AddCommand(newLeftoversListCmd())
	leftovers.AddCommand(newLeftoversShowCmd())
	return leftovers
}

func newLeftoversListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cook events with servings still remaining",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			events, err := store.ListOpenCookEvents(db)
			if err != nil {
				return fmt.Errorf("listing leftovers: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, events, func() output.Table {
				rows := make([][]string, 0, len(events))
				for _, e := range events {
					rows = append(rows, []string{
						fmt.Sprintf("%d", e.ID), e.RecipeEmoji + " " + e.RecipeTitle, e.CookedDate,
						fmt.Sprintf("%dd ago", e.DaysSinceCook),
						fmt.Sprintf("%.2f / %.2f", e.ServingsRemaining, e.ServingsYielded),
					})
				}
				return output.Table{Headers: []string{"ID", "RECIPE", "COOKED", "AGE", "REMAINING/YIELDED"}, Rows: rows}
			})
		},
	}
}

func newLeftoversShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <cook-event-id>",
		Short: "Show detail for one cook event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid cook event id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			d, err := store.GetCookEventDetail(db, id)
			if err != nil {
				return fmt.Errorf("cook event %d not found: %w", id, err)
			}
			return output.Render(os.Stdout, jsonOutput, d, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"recipe", d.RecipeEmoji + " " + d.RecipeTitle + " (" + d.RecipeID + ")"},
						{"cooked date", d.CookedDate},
						{"age", fmt.Sprintf("%dd", d.DaysSinceCook)},
						{"servings yielded", fmt.Sprintf("%.2f", d.ServingsYielded)},
						{"servings remaining", fmt.Sprintf("%.2f", d.ServingsRemaining)},
						{"total cost", fmt.Sprintf("$%.2f", d.TotalCost)},
					},
				}
			})
		},
	}
}
