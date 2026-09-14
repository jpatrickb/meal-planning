package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newTargetCmd() *cobra.Command {
	target := &cobra.Command{
		Use:   "target",
		Short: "Set or view daily macro targets per person",
	}
	target.AddCommand(newTargetSetCmd())
	target.AddCommand(newTargetShowCmd())
	return target
}

func newTargetSetCmd() *cobra.Command {
	var person string
	var kcal, protein, carbs, fat float64

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set a person's daily macro targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validatePerson(person); err != nil {
				return err
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			existing, _, err := store.GetTarget(db, person)
			if err != nil {
				return fmt.Errorf("loading existing target: %w", err)
			}
			t := store.Target{Person: person, Kcal: existing.Kcal, Protein: existing.Protein, Carbs: existing.Carbs, Fat: existing.Fat}
			if cmd.Flags().Changed("kcal") {
				t.Kcal = &kcal
			}
			if cmd.Flags().Changed("protein") {
				t.Protein = &protein
			}
			if cmd.Flags().Changed("carbs") {
				t.Carbs = &carbs
			}
			if cmd.Flags().Changed("fat") {
				t.Fat = &fat
			}

			if err := store.UpsertTarget(db, t); err != nil {
				return fmt.Errorf("saving target: %w", err)
			}
			return renderResult(map[string]any{"person": person, "updated": true}, "Updated targets for %s\n", person)
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "patrick or thea (required)")
	cmd.MarkFlagRequired("person")
	cmd.Flags().Float64Var(&kcal, "kcal", 0, "daily calorie target")
	cmd.Flags().Float64Var(&protein, "protein", 0, "daily protein target (g)")
	cmd.Flags().Float64Var(&carbs, "carbs", 0, "daily carb target (g)")
	cmd.Flags().Float64Var(&fat, "fat", 0, "daily fat target (g)")
	return cmd
}

func newTargetShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show configured macro targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			targets, err := store.ListTargets(db)
			if err != nil {
				return fmt.Errorf("listing targets: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, targets, func() output.Table {
				rows := make([][]string, 0, len(targets))
				for _, t := range targets {
					rows = append(rows, []string{t.Person, floatPtrStr(t.Kcal), floatPtrStr(t.Protein), floatPtrStr(t.Carbs), floatPtrStr(t.Fat)})
				}
				return output.Table{Headers: []string{"PERSON", "KCAL", "PROTEIN", "CARBS", "FAT"}, Rows: rows}
			})
		},
	}
}

func floatPtrStr(f *float64) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f", *f)
}
