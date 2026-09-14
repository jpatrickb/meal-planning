package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newAnalyticsCmd() *cobra.Command {
	analytics := &cobra.Command{
		Use:   "analytics",
		Short: "Cost, macro, waste, and recipe-variety analytics",
	}
	analytics.AddCommand(newAnalyticsCostCmd())
	analytics.AddCommand(newAnalyticsMacrosCmd())
	analytics.AddCommand(newAnalyticsWasteCmd())
	analytics.AddCommand(newAnalyticsVarietyCmd())
	return analytics
}

func addPeriodFlag(cmd *cobra.Command, period *string) {
	cmd.Flags().StringVar(period, "period", "month", "week, month, year, or all")
}

// addPersonFlag narrows a by-person breakdown to one person. The underlying
// figures are per-person either way; this only decides who is shown.
func addPersonFlag(cmd *cobra.Command, person *string) {
	cmd.Flags().StringVar(person, "person", "", "show only patrick or thea (default: everyone)")
}

// validateAnalyticsPerson allows an empty value, meaning "everyone".
func validateAnalyticsPerson(person string) error {
	if person == "" {
		return nil
	}
	return validatePerson(person)
}

func newAnalyticsCostCmd() *cobra.Command {
	var period, person string
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Total spend by person over a period",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateAnalyticsPerson(person); err != nil {
				return err
			}
			since, err := periodSince(period)
			if err != nil {
				return err
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			result, err := store.GetCostAnalytics(db, since)
			if err != nil {
				return fmt.Errorf("computing cost analytics: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				rows := make([][]string, 0, len(result.ByPerson)+1)
				for _, pc := range result.ByPerson {
					if person != "" && pc.Person != person {
						continue
					}
					rows = append(rows, []string{pc.Person, fmt.Sprintf("$%.2f", pc.Cost)})
				}
				// The household total is about the household, so it only
				// belongs in an unfiltered view.
				if person == "" {
					rows = append(rows, []string{"household total", fmt.Sprintf("$%.2f", result.Household)})
				}
				return output.Table{Headers: []string{"PERSON", "COST"}, Rows: rows}
			})
		},
	}
	addPeriodFlag(cmd, &period)
	addPersonFlag(cmd, &person)
	return cmd
}

func newAnalyticsMacrosCmd() *cobra.Command {
	var period, person string
	cmd := &cobra.Command{
		Use:   "macros",
		Short: "Average daily macros vs targets, by person, over a period",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateAnalyticsPerson(person); err != nil {
				return err
			}
			since, err := periodSince(period)
			if err != nil {
				return err
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			result, err := store.GetMacroAnalytics(db, since)
			if err != nil {
				return fmt.Errorf("computing macro analytics: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				rows := make([][]string, 0, len(result.ByPerson))
				for _, pm := range result.ByPerson {
					if person != "" && pm.Person != person {
						continue
					}
					target := "-"
					if pm.HasTarget && pm.Target.Kcal != nil {
						target = fmt.Sprintf("%.0f", *pm.Target.Kcal)
					}
					rows = append(rows, []string{
						pm.Person, fmt.Sprintf("%d", pm.DaysLogged),
						fmt.Sprintf("%.0f", pm.AvgCalories), target,
						fmt.Sprintf("%.0f", pm.AvgProtein), fmt.Sprintf("%.0f", pm.AvgCarbs), fmt.Sprintf("%.0f", pm.AvgFat),
					})
				}
				return output.Table{Headers: []string{"PERSON", "DAYS LOGGED", "AVG CAL", "TARGET CAL", "AVG PROTEIN", "AVG CARBS", "AVG FAT"}, Rows: rows}
			})
		},
	}
	addPeriodFlag(cmd, &period)
	addPersonFlag(cmd, &person)
	return cmd
}

func newAnalyticsWasteCmd() *cobra.Command {
	var period string
	cmd := &cobra.Command{
		Use: "waste",
		// No --person here: waste_log records what was discarded and why,
		// not who would have eaten it, so there is nothing to filter on.
		Short: "Food waste by reason over a period",
		RunE: func(cmd *cobra.Command, args []string) error {
			since, err := periodSince(period)
			if err != nil {
				return err
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			result, err := store.GetWasteAnalytics(db, since)
			if err != nil {
				return fmt.Errorf("computing waste analytics: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				rows := make([][]string, 0, len(result.ByReason)+1)
				for _, rw := range result.ByReason {
					rows = append(rows, []string{rw.Reason, fmt.Sprintf("%d", rw.Occurrences), fmt.Sprintf("$%.2f", rw.TotalCost)})
				}
				rows = append(rows, []string{"total", "", fmt.Sprintf("$%.2f", result.TotalCost)})
				return output.Table{Headers: []string{"REASON", "OCCURRENCES", "COST"}, Rows: rows}
			})
		},
	}
	addPeriodFlag(cmd, &period)
	return cmd
}

func newAnalyticsVarietyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "variety",
		Short: "Recipe rotation: what's neglected, what's rated well but underused",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			entries, err := store.GetVarietySummary(db, limit)
			if err != nil {
				return fmt.Errorf("computing variety summary: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, entries, func() output.Table {
				rows := make([][]string, 0, len(entries))
				for _, e := range entries {
					rating := "-"
					if e.Rating != nil {
						rating = fmt.Sprintf("%d", *e.Rating)
					}
					lastCooked := "never"
					if e.DaysSinceCooked != nil {
						lastCooked = fmt.Sprintf("%dd ago", *e.DaysSinceCooked)
					}
					rows = append(rows, []string{e.RecipeID, e.Title, rating, fmt.Sprintf("%d", e.TimesCooked), lastCooked})
				}
				return output.Table{Headers: []string{"ID", "TITLE", "RATING", "TIMES COOKED", "LAST COOKED"}, Rows: rows}
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max recipes to show (0 = no limit)")
	return cmd
}
