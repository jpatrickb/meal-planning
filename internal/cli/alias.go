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

func newAliasCmd() *cobra.Command {
	alias := &cobra.Command{
		Use:   "alias",
		Short: "Manage the receipt-text-to-pantry-item alias dictionary",
	}
	alias.AddCommand(newAliasListCmd())
	alias.AddCommand(newAliasConfirmCmd())
	return alias
}

func newAliasListCmd() *cobra.Command {
	var unconfirmed bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List aliases, optionally only ones awaiting confirmation",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			aliases, err := store.ListAliases(db, unconfirmed)
			if err != nil {
				return fmt.Errorf("listing aliases: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, aliases, func() output.Table {
				rows := make([][]string, 0, len(aliases))
				for _, a := range aliases {
					rows = append(rows, []string{
						fmt.Sprintf("%d", a.ID), a.RawTextNormalized, fmt.Sprintf("%d %s", a.ItemID, a.ItemName),
						confidencePtrStr(a.Confidence), boolStr(a.Confirmed), fmt.Sprintf("%d", a.TimesMatched),
					})
				}
				return output.Table{Headers: []string{"ID", "RAW TEXT", "ITEM", "CONFIDENCE", "CONFIRMED", "TIMES MATCHED"}, Rows: rows}
			})
		},
	}
	cmd.Flags().BoolVar(&unconfirmed, "unconfirmed", false, "show only pending (unconfirmed) aliases")
	return cmd
}

func confidencePtrStr(f *float64) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *f*100)
}

func newAliasConfirmCmd() *cobra.Command {
	var raw string
	var itemID int64
	var createItem bool
	var name, category, unit string

	cmd := &cobra.Command{
		Use:   "confirm [alias-id]",
		Short: "Confirm (or create) an alias, pointing it at a pantry item",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && raw == "" {
				return fmt.Errorf("provide either an alias id argument or --raw \"<receipt text>\"")
			}
			if len(args) == 1 && raw != "" {
				return fmt.Errorf("provide an alias id OR --raw, not both")
			}
			if !createItem && !cmd.Flags().Changed("item-id") {
				return fmt.Errorf("provide --item-id or --create-item")
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			resolvedItemID := itemID
			var resolvedItemName string
			if createItem {
				if name == "" || category == "" || unit == "" {
					return fmt.Errorf("--create-item requires --name, --category, and --unit")
				}
				if err := validateCategory(category); err != nil {
					return err
				}
				if existing, ok, err := store.GetPantryItemByName(db, name); err != nil {
					return fmt.Errorf("checking for existing item: %w", err)
				} else if ok {
					return fmt.Errorf("item %q already exists (id %d) - use --item-id %d instead", name, existing.ID, existing.ID)
				}
				newID, err := store.CreatePantryItem(db, name, category, unit)
				if err != nil {
					return fmt.Errorf("creating pantry item: %w", err)
				}
				resolvedItemID = newID
				resolvedItemName = name
			} else {
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found: %w", itemID, err)
				}
				resolvedItemName = item.Name
			}

			var aliasID int64
			if len(args) == 1 {
				aliasID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("invalid alias id %q", args[0])
				}
				if _, err := store.GetAlias(db, aliasID); err != nil {
					return fmt.Errorf("alias %d not found: %w", aliasID, err)
				}
				if err := store.ConfirmAlias(db, aliasID, resolvedItemID); err != nil {
					return fmt.Errorf("confirming alias: %w", err)
				}
			} else {
				normalized := domain.NormalizeReceiptText(raw)
				aliasID, err = store.UpsertConfirmedAlias(db, normalized, nil, resolvedItemID)
				if err != nil {
					return fmt.Errorf("recording alias: %w", err)
				}
			}

			return renderResult(
				map[string]any{"alias_id": aliasID, "item_id": resolvedItemID, "item": resolvedItemName},
				"Alias %d confirmed -> item %d (%s)\n", aliasID, resolvedItemID, resolvedItemName)
		},
	}
	cmd.Flags().StringVar(&raw, "raw", "", "verbatim receipt text (alternative to a numeric alias id)")
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "point the alias at this existing pantry item")
	cmd.Flags().BoolVar(&createItem, "create-item", false, "create a new pantry item and point the alias at it")
	cmd.Flags().StringVar(&name, "name", "", "new item name (with --create-item)")
	cmd.Flags().StringVar(&category, "category", "", "new item category (with --create-item)")
	cmd.Flags().StringVar(&unit, "unit", "", "new item default unit (with --create-item)")
	return cmd
}
