package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

var validWasteReasons = map[string]bool{
	"spoiled": true, "expired": true, "too_much_cooked": true, "disliked": true, "other": true,
}

func newWasteCmd() *cobra.Command {
	waste := &cobra.Command{
		Use:   "waste",
		Short: "Log discarded food",
	}
	waste.AddCommand(newWasteLogCmd())
	waste.AddCommand(newWasteVoidCmd())
	return waste
}

func newWasteLogCmd() *cobra.Command {
	var itemID, cookEventID int64
	var qty, cost float64
	var unit, reason, date, description string

	cmd := &cobra.Command{
		Use:   "log",
		Short: "Log wasted pantry stock (--item-id) or wasted leftovers (--cook-event-id)",
		RunE: func(cmd *cobra.Command, args []string) error {
			hasItem := cmd.Flags().Changed("item-id")
			hasCookEvent := cmd.Flags().Changed("cook-event-id")
			if hasItem == hasCookEvent {
				return fmt.Errorf("provide exactly one of --item-id or --cook-event-id")
			}
			if qty <= 0 {
				return fmt.Errorf("--qty must be positive")
			}
			if !validWasteReasons[reason] {
				return fmt.Errorf("invalid --reason %q: must be spoiled, expired, too_much_cooked, disliked, or other", reason)
			}
			if date == "" {
				date = todayDate()
			}
			var descPtr *string
			if description != "" {
				descPtr = &description
			}
			var costPtr *float64
			if cmd.Flags().Changed("cost") {
				costPtr = &cost
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var id int64
			if hasItem {
				if unit == "" {
					item, err := store.GetPantryItem(db, itemID)
					if err != nil {
						return fmt.Errorf("item %d not found: %w", itemID, err)
					}
					unit = item.DefaultUnit
				}
				id, err = store.LogItemWaste(db, itemID, qty, unit, reason, date, descPtr, costPtr, domain.UnitConverterFor(db, itemID))
			} else {
				id, err = store.LogLeftoverWaste(db, cookEventID, qty, reason, date, descPtr, costPtr)
			}
			if err != nil {
				return fmt.Errorf("logging waste: %w", err)
			}

			return output.Render(os.Stdout, jsonOutput, map[string]any{"waste_id": id}, func() output.Table {
				return output.Table{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{{"waste id", fmt.Sprintf("%d", id)}}}
			})
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item id (mutually exclusive with --cook-event-id)")
	cmd.Flags().Int64Var(&cookEventID, "cook-event-id", 0, "cook event id, for wasted leftovers (mutually exclusive with --item-id)")
	cmd.Flags().Float64Var(&qty, "qty", 0, "quantity discarded (required)")
	cmd.MarkFlagRequired("qty")
	cmd.Flags().StringVar(&unit, "unit", "", "unit (default: item's default_unit, for --item-id)")
	cmd.Flags().StringVar(&reason, "reason", "", "spoiled, expired, too_much_cooked, disliked, or other (required)")
	cmd.MarkFlagRequired("reason")
	cmd.Flags().StringVar(&date, "date", "", "date discarded, YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&description, "description", "", "free-text note")
	cmd.Flags().Float64Var(&cost, "cost", 0, "override the derived cost impact")
	return cmd
}

// newWasteVoidCmd undoes a waste_log row logged in error - the wrong item,
// the wrong quantity, or food that turned out to be fine after all.
func newWasteVoidCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "void <waste-log-id>",
		Short: "Reverse a waste entry logged in error, putting back the stock or servings it discarded",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid waste log id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			v, err := store.VoidWasteEntry(db, id)
			if err != nil {
				return err
			}
			if err := output.Render(os.Stdout, jsonOutput, v, func() output.Table {
				rows := [][]string{
					{"deleted waste entry", fmt.Sprintf("%d", v.ID)},
					{"date", v.WastedDate},
					{"reason", v.Reason},
					{"cost removed", fmt.Sprintf("$%.2f", v.EstimatedCost)},
				}
				if v.ItemName != "" {
					rows = append(rows, []string{"item", v.ItemName})
				}
				for _, r := range v.RestoredLots {
					rows = append(rows, []string{"stock restored",
						fmt.Sprintf("%s: %.3f %s (lot %d)", r.ItemName, r.Quantity, r.Unit, r.StockID)})
				}
				if v.ServingsReturned != 0 {
					rows = append(rows, []string{"servings returned",
						fmt.Sprintf("%.2f to cook event %d", v.ServingsReturned, *v.CookEventID)})
				}
				return output.Table{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
			}); err != nil {
				return err
			}
			if !jsonOutput && v.Unrestored > 1e-9 {
				fmt.Printf("\nNOTE: %.3f %s could not be put back - no discarded lot of %s in that unit.\n",
					v.Unrestored, v.Unit, v.ItemName)
				fmt.Println("Add it by hand with `meal pantry adjust` if the stock really is still there.")
			}
			return nil
		},
	}
}
