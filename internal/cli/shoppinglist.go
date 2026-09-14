package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newShoppingListCmd() *cobra.Command {
	sl := &cobra.Command{
		Use:   "shopping-list",
		Short: "Maintain the shopping checklist",
	}
	sl.AddCommand(newShoppingListBuildCmd())
	sl.AddCommand(newShoppingListShowCmd())
	sl.AddCommand(newShoppingListAddCmd())
	sl.AddCommand(newShoppingListCheckCmd())
	sl.AddCommand(newShoppingListRemoveCmd())
	sl.AddCommand(newShoppingListClearCmd())
	return sl
}

func newShoppingListBuildCmd() *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Seed the list from planned recipes' ingredient lines (default: today through 6 days out)",
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

			recipeIDs, err := store.ListPlannedRecipeIDs(db, from, to)
			if err != nil {
				return fmt.Errorf("loading planned recipes: %w", err)
			}
			for _, id := range recipeIDs {
				recipe, err := store.GetRecipe(db, id)
				if err != nil {
					return fmt.Errorf("loading recipe %q: %w", id, err)
				}
				var lines []string
				for _, g := range recipe.IngredientGroups {
					lines = append(lines, g.Items...)
				}
				if err := store.RebuildRecipeShoppingListItems(db, id, lines); err != nil {
					return fmt.Errorf("seeding items for %q: %w", id, err)
				}
			}
			return renderResult(
				map[string]any{"recipes": len(recipeIDs), "from": from, "to": to},
				"Seeded shopping list from %d planned recipe(s) (%s to %s)\n", len(recipeIDs), from, to)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (default: 6 days out)")
	return cmd
}

func newShoppingListShowCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the shopping list (unchecked items by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			items, err := store.ListShoppingListItems(db, all)
			if err != nil {
				return fmt.Errorf("listing shopping list: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, items, func() output.Table {
				rows := make([][]string, 0, len(items))
				for _, it := range items {
					source := "manual"
					if it.SourceRecipeID != nil {
						source = *it.SourceRecipeID
					}
					rows = append(rows, []string{fmt.Sprintf("%d", it.ID), it.Description, source, boolStr(it.Checked)})
				}
				return output.Table{Headers: []string{"ID", "ITEM", "SOURCE", "CHECKED"}, Rows: rows}
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include already-checked items")
	return cmd
}

func newShoppingListAddCmd() *cobra.Command {
	var recipe string
	cmd := &cobra.Command{
		Use:   "add <description>",
		Short: "Add one manual item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var recipePtr *string
			if recipe != "" {
				if _, err := store.GetRecipe(db, recipe); err != nil {
					return fmt.Errorf("recipe %q not found: %w", recipe, err)
				}
				recipePtr = &recipe
			}
			id, err := store.AddShoppingListItem(db, args[0], recipePtr)
			if err != nil {
				return fmt.Errorf("adding item: %w", err)
			}
			return renderResult(
				map[string]any{"item_id": id, "description": args[0]},
				"Added item %d: %s\n", id, args[0])
		},
	}
	cmd.Flags().StringVar(&recipe, "recipe", "", "tag this item as sourced from a recipe")
	return cmd
}

func newShoppingListCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <id>",
		Short: "Mark an item checked (bought)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid item id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if err := store.CheckShoppingListItem(db, id); err != nil {
				return fmt.Errorf("item %d not found: %w", id, err)
			}
			return renderResult(map[string]any{"item_id": id, "checked": true}, "Checked item %d\n", id)
		},
	}
}

// newShoppingListRemoveCmd deletes a line outright, for something added by
// mistake. `check` is not the same thing - that records a purchase - and
// `clear` would take the rest of the list with it.
func newShoppingListRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove an item added by mistake (not the same as checking it off)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid item id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if err := store.DeleteShoppingListItem(db, id); err != nil {
				return err
			}
			return renderResult(map[string]any{"item_id": id, "removed": true}, "Removed item %d\n", id)
		},
	}
}

func newShoppingListClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove every item from the list",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			n, err := store.ClearShoppingList(db)
			if err != nil {
				return fmt.Errorf("clearing list: %w", err)
			}
			return renderResult(map[string]any{"removed": n}, "Cleared %d item(s)\n", n)
		},
	}
}
