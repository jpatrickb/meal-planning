package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newPlanCmd() *cobra.Command {
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Set or view the meal plan",
	}
	plan.AddCommand(newPlanSetCmd())
	plan.AddCommand(newPlanShowCmd())
	return plan
}

func newPlanSetCmd() *cobra.Command {
	var date, slot, recipe, freeText, plannedFor string

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Plan (or replace) one meal slot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if date == "" {
				return fmt.Errorf("--date is required")
			}
			slotPtr, err := validateMealSlot(slot)
			if err != nil {
				return err
			}
			if slotPtr == nil {
				return fmt.Errorf("--slot is required")
			}
			if (recipe == "") == (freeText == "") {
				return fmt.Errorf("provide exactly one of --recipe or --free-text")
			}
			if plannedFor == "" {
				plannedFor = "both"
			}
			if plannedFor != "patrick" && plannedFor != "thea" && plannedFor != "both" {
				return fmt.Errorf("invalid --for %q: must be patrick, thea, or both", plannedFor)
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			e := store.MealPlanEntry{PlanDate: date, MealSlot: slot, PlannedFor: plannedFor}
			if recipe != "" {
				if _, err := store.GetRecipe(db, recipe); err != nil {
					return fmt.Errorf("recipe %q not found: %w", recipe, err)
				}
				e.RecipeID = &recipe
			} else {
				e.FreeTextMeal = &freeText
			}

			id, err := store.UpsertPlanEntry(db, e)
			if err != nil {
				return fmt.Errorf("saving plan entry: %w", err)
			}
			return renderResult(
				map[string]any{"plan_entry_id": id, "date": date, "slot": slot, "for": plannedFor},
				"Planned entry %d: %s %s\n", id, date, slot)
		},
	}
	cmd.Flags().StringVar(&date, "date", "", "date, YYYY-MM-DD (required)")
	cmd.Flags().StringVar(&slot, "slot", "", "breakfast, lunch, dinner, or snack (required)")
	cmd.Flags().StringVar(&recipe, "recipe", "", "recipe slug (mutually exclusive with --free-text)")
	cmd.Flags().StringVar(&freeText, "free-text", "", "free-text meal description (mutually exclusive with --recipe)")
	cmd.Flags().StringVar(&plannedFor, "for", "both", "patrick, thea, or both")
	return cmd
}

func newPlanShowCmd() *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the plan for a date range (default: today through 6 days out)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" {
				from = todayDate()
			}
			if to == "" {
				to = time.Now().AddDate(0, 0, 6).Format("2006-01-02")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			entries, err := store.ListPlanEntries(db, from, to)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, entries, func() output.Table {
				rows := make([][]string, 0, len(entries))
				for _, e := range entries {
					meal := e.RecipeTitle
					if meal == "" && e.FreeTextMeal != nil {
						meal = *e.FreeTextMeal
					}
					rows = append(rows, []string{e.PlanDate, e.MealSlot, meal, e.PlannedFor, e.Status})
				}
				return output.Table{Headers: []string{"DATE", "SLOT", "MEAL", "FOR", "STATUS"}, Rows: rows}
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (default: 6 days out)")
	return cmd
}
