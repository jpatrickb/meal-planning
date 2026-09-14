package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newShopCmd() *cobra.Command {
	shop := &cobra.Command{
		Use:   "shop",
		Short: "Record a grocery purchase, line item by line item",
	}
	shop.AddCommand(newShopStartCmd())
	shop.AddCommand(newShopAddItemCmd())
	shop.AddCommand(newShopShowCmd())
	shop.AddCommand(newShopFinishCmd())
	return shop
}

func newShopStartCmd() *cobra.Command {
	var storeName, date string
	var receiptTotal float64

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new purchase (receipt)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if date == "" {
				date = todayDate()
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			p := store.Purchase{PurchaseDate: date}
			if storeName != "" {
				p.Store = &storeName
			}
			if cmd.Flags().Changed("receipt-total") {
				p.ReceiptTotal = &receiptTotal
			}
			id, err := store.CreatePurchase(db, p)
			if err != nil {
				return fmt.Errorf("starting purchase: %w", err)
			}
			return renderResult(
				map[string]any{"purchase_id": id, "date": date, "store": storeName},
				"Started purchase %d (%s, %s)\n", id, date, orDash(storeName))
		},
	}
	cmd.Flags().StringVar(&storeName, "store", "", "store name")
	cmd.Flags().StringVar(&date, "date", "", "purchase date, YYYY-MM-DD (default: today)")
	cmd.Flags().Float64Var(&receiptTotal, "receipt-total", 0, "total on the receipt, for reconciliation")
	return cmd
}

func newShopAddItemCmd() *cobra.Command {
	var purchaseID, itemID int64
	var raw, upc, unit, name, category string
	var qty, price float64
	var createNew bool

	cmd := &cobra.Command{
		Use:   "add-item",
		Short: "Add one receipt line item to an open purchase",
		RunE: func(cmd *cobra.Command, args []string) error {
			if raw == "" {
				return fmt.Errorf("--raw is required")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			purchase, err := store.GetPurchase(db, purchaseID)
			if err != nil {
				return fmt.Errorf("purchase %d not found: %w", purchaseID, err)
			}
			if purchase.Status != "open" {
				return fmt.Errorf("purchase %d is already finished", purchaseID)
			}

			pi := store.PurchaseItem{PurchaseID: purchaseID, RawText: raw}
			if upc != "" {
				pi.UPC = &upc
			}
			if cmd.Flags().Changed("qty") {
				pi.Quantity = &qty
			}
			if unit != "" {
				pi.Unit = &unit
			}
			if cmd.Flags().Changed("price") {
				pi.UnitPrice = &price
				if cmd.Flags().Changed("qty") {
					total := price * qty
					pi.LineTotal = &total
				} else {
					pi.LineTotal = &price
				}
			}

			// A package size stated in the receipt text that disagrees with
			// --qty is almost always a whole package logged as one unit,
			// which silently puts the entire line's price on a single ounce.
			// Warn rather than block: a line genuinely can read "32 oz" and
			// describe one of several such packages.
			if cmd.Flags().Changed("qty") && unit != "" {
				if stated, ok := domain.StatedPackageSize(raw, unit); ok && stated != qty {
					fmt.Fprintf(os.Stderr,
						"warning: this line's text states %g %s but --qty is %g. If the price covers the whole package, use --qty %g so cost per %s comes out right.\n",
						stated, domain.CanonicalUnit(unit), qty, stated, domain.CanonicalUnit(unit))
				}
			}

			normalized := domain.NormalizeReceiptText(raw)
			var upcPtr *string
			if upc != "" {
				upcPtr = &upc
			}
			var status string

			switch {
			case createNew:
				if name == "" || category == "" || unit == "" {
					return fmt.Errorf("--create-new requires --name, --category, and --unit")
				}
				if err := validateCategory(category); err != nil {
					return err
				}
				if existing, ok, err := store.GetPantryItemByName(db, name); err != nil {
					return fmt.Errorf("checking for existing item: %w", err)
				} else if ok {
					return fmt.Errorf("item %q already exists (id %d) - use --item-id %d instead of --create-new", name, existing.ID, existing.ID)
				}
				newID, err := store.CreatePantryItem(db, name, category, unit)
				if err != nil {
					return fmt.Errorf("creating pantry item: %w", err)
				}
				pi.MatchedItemID = &newID
				method := "manual"
				pi.MatchMethod = &method
				if _, err := store.UpsertConfirmedAlias(db, normalized, upcPtr, newID); err != nil {
					return fmt.Errorf("recording alias: %w", err)
				}
				status = fmt.Sprintf("matched to new item %d (%s) - future purchases of this exact line will auto-match", newID, name)
			case cmd.Flags().Changed("item-id"):
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found: %w", itemID, err)
				}
				pi.MatchedItemID = &itemID
				method := "manual"
				pi.MatchMethod = &method
				if _, err := store.UpsertConfirmedAlias(db, normalized, upcPtr, itemID); err != nil {
					return fmt.Errorf("recording alias: %w", err)
				}
				status = fmt.Sprintf("matched to item %d (%s) - future purchases of this exact line will auto-match", itemID, item.Name)
			default:
				result, err := domain.MatchPurchaseLine(db, raw, upc)
				if err != nil {
					return fmt.Errorf("matching against known items/aliases: %w", err)
				}
				pi.MatchedItemID = result.ItemID
				pi.MatchMethod = &result.Method
				conf := result.Confidence
				pi.MatchConfidence = &conf
				switch result.Method {
				case "upc", "exact_alias":
					status = fmt.Sprintf("auto-matched (%s) to item %d", result.Method, *result.ItemID)
				case "fuzzy":
					status = fmt.Sprintf("fuzzy match candidate (%.0f%% confidence, alias id %d) - NOT stocked yet; confirm with `meal alias confirm %d --item-id <id>` or re-add with an explicit --item-id", result.Confidence*100, *result.PendingAliasID, *result.PendingAliasID)
				default:
					status = "unmatched - re-run with --item-id or --create-new, or use `meal alias confirm` after reviewing"
				}
			}

			id, err := store.AddPurchaseItem(db, pi)
			if err != nil {
				return fmt.Errorf("adding purchase item: %w", err)
			}
			fmt.Printf("Added purchase item %d: %s\n", id, status)
			return nil
		},
	}
	cmd.Flags().Int64Var(&purchaseID, "purchase", 0, "purchase id from `meal shop start` (required)")
	cmd.MarkFlagRequired("purchase")
	cmd.Flags().StringVar(&raw, "raw", "", "verbatim receipt line text (required)")
	cmd.Flags().StringVar(&upc, "upc", "", "UPC/barcode if available")
	cmd.Flags().Float64Var(&qty, "qty", 0, "quantity purchased")
	cmd.Flags().StringVar(&unit, "unit", "", "unit (required with --create-new)")
	cmd.Flags().Float64Var(&price, "price", 0, "unit price (or line total if --qty is omitted)")
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "match to this existing pantry item id")
	cmd.Flags().BoolVar(&createNew, "create-new", false, "create a new pantry item for this line")
	cmd.Flags().StringVar(&name, "name", "", "new item name (with --create-new)")
	cmd.Flags().StringVar(&category, "category", "", "new item category (with --create-new)")
	return cmd
}

func newShopShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <purchase-id>",
		Short: "Show a purchase and its line items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid purchase id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			purchase, err := store.GetPurchase(db, id)
			if err != nil {
				return fmt.Errorf("purchase %d not found: %w", id, err)
			}
			items, err := store.ListPurchaseItems(db, id)
			if err != nil {
				return fmt.Errorf("loading items: %w", err)
			}

			type shopShowResult struct {
				Purchase store.Purchase       `json:"purchase"`
				Items    []store.PurchaseItem `json:"items"`
			}
			result := shopShowResult{Purchase: purchase, Items: items}

			if jsonOutput {
				return output.Render(os.Stdout, true, result, nil)
			}
			fmt.Printf("Purchase %d: %s at %s (%s)\n", purchase.ID, purchase.PurchaseDate, orDashPtr(purchase.Store), purchase.Status)
			rows := make([][]string, 0, len(items))
			for _, it := range items {
				matched := "unmatched"
				if it.MatchedItemID != nil {
					matched = fmt.Sprintf("item %d", *it.MatchedItemID)
				}
				rows = append(rows, []string{fmt.Sprintf("%d", it.ID), it.RawText, floatPtrStr(it.Quantity), orDashPtr(it.Unit), floatPtrStr(it.UnitPrice), matched})
			}
			return output.RenderTable(os.Stdout, output.Table{Headers: []string{"ID", "RAW TEXT", "QTY", "UNIT", "PRICE", "MATCH"}, Rows: rows})
		},
	}
}

func newShopFinishCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "finish <purchase-id>",
		Short: "Finish a purchase: create pantry stock lots for every matched item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid purchase id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			result, err := store.FinishPurchase(db, id)
			if err != nil {
				return fmt.Errorf("finishing purchase: %w", err)
			}
			if len(result.NoShelfLifeItems) > 0 {
				fmt.Fprintf(os.Stderr, "warning: no typical shelf life set for: %s - no expiration date recorded for these (set with `meal pantry set-shelf-life --item-id N --days X`)\n",
					strings.Join(result.NoShelfLifeItems, ", "))
			}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"stocked", fmt.Sprintf("%d", result.StockedCount)},
						{"unmatched (not stocked)", fmt.Sprintf("%d", result.UnmatchedCount)},
					},
				}
			})
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orDashPtr(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
