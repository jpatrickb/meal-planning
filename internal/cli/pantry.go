package cli

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newPantryCmd() *cobra.Command {
	pantry := &cobra.Command{
		Use:   "pantry",
		Short: "View and adjust on-hand pantry stock",
	}
	pantry.AddCommand(newPantryListCmd())
	pantry.AddCommand(newPantryAdjustCmd())
	pantry.AddCommand(newPantrySetConversionCmd())
	pantry.AddCommand(newPantrySetShelfLifeCmd())
	pantry.AddCommand(newPantryLinkNutritionCmd())
	pantry.AddCommand(newPantryMergeCmd())
	pantry.AddCommand(newPantrySetDefaultUnitCmd())
	pantry.AddCommand(newPantryBackfillExpiryCmd())
	pantry.AddCommand(newPantrySetLotPriceCmd())
	pantry.AddCommand(newPantrySetNutritionCmd())
	pantry.AddCommand(newPantryMoveLotCmd())
	return pantry
}

func newPantryListCmd() *cobra.Command {
	var expiringWithin int
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List on-hand pantry stock, or lots expiring soon",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if all {
				items, err := store.ListAllPantryItems(db)
				if err != nil {
					return fmt.Errorf("listing pantry catalog: %w", err)
				}
				return output.Render(os.Stdout, jsonOutput, items, func() output.Table {
					rows := make([][]string, 0, len(items))
					for _, it := range items {
						rows = append(rows, []string{fmt.Sprintf("%d", it.ID), it.Name, it.Category, it.DefaultUnit})
					}
					return output.Table{Headers: []string{"ITEM ID", "NAME", "CATEGORY", "DEFAULT UNIT"}, Rows: rows}
				})
			}

			if cmd.Flags().Changed("expiring-within") {
				lots, err := store.ListExpiringStock(db, expiringWithin)
				if err != nil {
					return fmt.Errorf("listing expiring stock: %w", err)
				}
				return output.Render(os.Stdout, jsonOutput, lots, func() output.Table {
					rows := make([][]string, 0, len(lots))
					for _, l := range lots {
						rows = append(rows, []string{
							fmt.Sprintf("%d", l.StockID), l.ItemName, l.Category,
							fmt.Sprintf("%.2f %s", l.Quantity, l.Unit), l.ExpiresDate, fmt.Sprintf("%d", l.DaysUntilExpiry),
						})
					}
					return output.Table{Headers: []string{"STOCK ID", "ITEM", "CATEGORY", "QTY", "EXPIRES", "DAYS LEFT"}, Rows: rows}
				})
			}

			items, err := store.ListPantryOnHand(db)
			if err != nil {
				return fmt.Errorf("listing pantry: %w", err)
			}
			return output.Render(os.Stdout, jsonOutput, items, func() output.Table {
				rows := make([][]string, 0, len(items))
				for _, it := range items {
					expiry := "-"
					if it.EarliestExpiry != nil {
						expiry = *it.EarliestExpiry
					}
					rows = append(rows, []string{
						fmt.Sprintf("%d", it.ItemID), it.Name, it.Category,
						fmt.Sprintf("%.2f %s", it.Quantity, it.Unit), expiry,
					})
				}
				return output.Table{Headers: []string{"ITEM ID", "NAME", "CATEGORY", "ON HAND", "SOONEST EXPIRY"}, Rows: rows}
			})
		},
	}
	cmd.Flags().IntVar(&expiringWithin, "expiring-within", 7, "show only lots expiring within N days")
	cmd.Flags().BoolVar(&all, "all", false, "list the full item catalog regardless of on-hand stock (for browsing/reuse-checking while it's being built)")
	return cmd
}

func newPantryAdjustCmd() *cobra.Command {
	var itemID int64
	var qty, price float64
	var reason string
	var unit, expiresDate string

	cmd := &cobra.Command{
		Use:   "adjust",
		Short: "Manually adjust on-hand quantity for an item (+add a lot, -consume FIFO)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if qty == 0 {
				return fmt.Errorf("--qty must be nonzero")
			}
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			if qty < 0 && cmd.Flags().Changed("price") {
				return fmt.Errorf("--price only applies when adding stock (positive --qty)")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			item, err := store.GetPantryItem(db, itemID)
			if err != nil {
				return fmt.Errorf("item %d not found (try `meal pantry list`): %w", itemID, err)
			}
			if unit == "" {
				unit = item.DefaultUnit
			}

			tx, err := db.Begin()
			if err != nil {
				return fmt.Errorf("starting transaction: %w", err)
			}
			defer tx.Rollback()

			if qty > 0 {
				stock := store.PantryStock{ItemID: itemID, Quantity: qty, Unit: unit, AcquiredDate: todayDate()}
				if cmd.Flags().Changed("price") {
					stock.UnitCost = &price
				}
				if cmd.Flags().Changed("expires-date") {
					stock.ExpiresDate = &expiresDate
				} else if life, ok, err := store.GetShelfLife(db, itemID); err != nil {
					return fmt.Errorf("checking shelf life: %w", err)
				} else if ok {
					expires, err := store.EstimateExpiryDate(stock.AcquiredDate, life.Days)
					if err != nil {
						return fmt.Errorf("estimating expiry: %w", err)
					}
					stock.ExpiresDate = &expires
				} else {
					fmt.Fprintf(os.Stderr, "warning: no typical shelf life set for %s, no expiration date recorded (set with `meal pantry set-shelf-life --item-id %d --days N`)\n", item.Name, itemID)
				}
				if _, err := store.AddPantryStock(tx, stock); err != nil {
					return fmt.Errorf("adding stock: %w", err)
				}
			} else {
				if _, _, err := store.DecrementPantryStockFIFO(tx, itemID, unit, -qty, "consumed", domain.UnitConverterFor(db, itemID)); err != nil {
					return err
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing: %w", err)
			}

			return renderResult(
				map[string]any{"item_id": itemID, "item": item.Name, "delta": qty, "unit": unit, "reason": reason},
				"Adjusted %s by %+.2f %s (reason: %s)\n", item.Name, qty, unit, reason)
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item id (required, see `meal pantry list`)")
	cmd.MarkFlagRequired("item-id")
	cmd.Flags().Float64Var(&qty, "qty", 0, "signed quantity change: positive adds a lot, negative consumes FIFO (required)")
	cmd.MarkFlagRequired("qty")
	cmd.Flags().StringVar(&reason, "reason", "", "why this adjustment is happening (required)")
	cmd.MarkFlagRequired("reason")
	cmd.Flags().StringVar(&unit, "unit", "", "unit for this adjustment (default: item's default_unit)")
	cmd.Flags().Float64Var(&price, "price", 0, "unit cost for a new lot (only valid with positive --qty)")
	cmd.Flags().StringVar(&expiresDate, "expires-date", "", "explicit expiration date, YYYY-MM-DD (default: estimated from the item's typical shelf life, if set)")
	return cmd
}

func newPantrySetConversionCmd() *cobra.Command {
	var itemID int64
	var fromUnit, toUnit string
	var factor float64
	var universal, estimated bool

	cmd := &cobra.Command{
		Use:   "set-conversion",
		Short: "Record a unit-conversion factor (e.g. 1 carrot ≈ 61g), used to bridge units in costing, pantry decrements and macros",
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromUnit == "" || toUnit == "" {
				return fmt.Errorf("--from and --to are required")
			}
			if factor <= 0 {
				return fmt.Errorf("--factor must be positive")
			}
			if universal == cmd.Flags().Changed("item-id") {
				return fmt.Errorf("specify exactly one of --item-id or --universal")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var itemIDPtr *int64
			if !universal {
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found (try `meal pantry list`): %w", itemID, err)
				}
				itemIDPtr = &item.ID
			}

			// Warn before writing, not after: a contradiction is only
			// obvious while both numbers are in front of you.
			if implied, disagrees, cerr := domain.CheckConversionAgreement(db, itemIDPtr, fromUnit, toUnit, factor); cerr == nil && disagrees {
				fmt.Fprintf(os.Stderr,
					"warning: this contradicts what's already recorded - existing facts imply 1 %s = %.4f %s, not %.4f. Saving anyway; reconcile the others with `meal pantry set-conversion` so every unit agrees.\n",
					domain.CanonicalUnit(fromUnit), implied, domain.CanonicalUnit(toUnit), factor)
			}

			id, err := store.CreateUnitConversion(db, itemIDPtr, domain.CanonicalUnit(fromUnit), domain.CanonicalUnit(toUnit), factor)
			if err != nil {
				return fmt.Errorf("saving conversion: %w", err)
			}
			source := "patrick_provided"
			if estimated {
				source = "estimated"
			}
			if err := store.SetUnitConversionSource(db, id, source); err != nil {
				return fmt.Errorf("recording conversion source: %w", err)
			}
			scope := fmt.Sprintf("item %d", itemID)
			if universal {
				scope = "universal"
			}
			return renderResult(
				map[string]any{"conversion_id": id, "from": domain.CanonicalUnit(fromUnit), "to": domain.CanonicalUnit(toUnit), "factor": factor, "scope": scope, "source": source},
				"Saved conversion %d: 1 %s = %.4f %s (%s, %s)\n", id, domain.CanonicalUnit(fromUnit), factor, domain.CanonicalUnit(toUnit), scope, source)
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item this conversion applies to (mutually exclusive with --universal)")
	cmd.Flags().BoolVar(&universal, "universal", false, "this conversion applies to any item (e.g. cup -> ml), not one specific pantry item")
	cmd.Flags().StringVar(&fromUnit, "from", "", "source unit (required)")
	cmd.Flags().StringVar(&toUnit, "to", "", "target unit (required)")
	cmd.Flags().Float64Var(&factor, "factor", 0, "quantity(to) = quantity(from) * factor (required, must be positive)")
	cmd.Flags().BoolVar(&estimated, "estimated", false, "this factor is a standard reference figure, not measured or read off a package")
	return cmd
}

// newPantrySetNutritionCmd records macros straight off a product label.
// USDA has no entry for many branded items, and its nearest generic match
// can be meaningfully different - when the package is in hand, its own label
// is the better source, and this takes precedence over the item's fdc_id
// link everywhere macros get computed.
func newPantrySetNutritionCmd() *cobra.Command {
	var itemID int64
	var servingGrams, calories, protein, carbs, fat, fiber float64
	var note string
	var clear, patrickProvided, estimated bool

	cmd := &cobra.Command{
		Use:   "set-nutrition",
		Short: "Record macros for a pantry item from its product label, overriding its USDA link",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("item-id") {
				return fmt.Errorf("--item-id is required")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			item, err := store.GetPantryItem(db, itemID)
			if err != nil {
				return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
			}

			if clear {
				if err := store.ClearItemNutrition(db, itemID); err != nil {
					return fmt.Errorf("clearing label nutrition: %w", err)
				}
				return renderResult(
					map[string]any{"item_id": itemID, "item": item.Name, "cleared": true},
					"Cleared label nutrition for %s (its USDA link, if any, applies again)\n", item.Name)
			}
			if servingGrams <= 0 {
				return fmt.Errorf("--serving-grams must be positive: the label's numbers are per serving, and storing them per 100g needs the serving's gram weight")
			}

			if patrickProvided && estimated {
				return fmt.Errorf("--patrick-provided and --estimated are mutually exclusive")
			}
			scale := 100 / servingGrams
			source := "label"
			switch {
			case patrickProvided:
				source = "patrick_provided"
			case estimated:
				source = "estimated"
			}
			var notePtr *string
			if note != "" {
				notePtr = &note
			}
			entry := store.ItemNutrition{
				ItemID: itemID, CaloriesPer100g: calories * scale, ProteinPer100g: protein * scale,
				CarbsPer100g: carbs * scale, FatPer100g: fat * scale, FiberPer100g: fiber * scale,
				Source: source, Note: notePtr,
			}
			if err := store.UpsertItemNutrition(db, entry); err != nil {
				return fmt.Errorf("saving label nutrition: %w", err)
			}

			return output.Render(os.Stdout, jsonOutput, entry, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"item", item.Name},
						{"serving", fmt.Sprintf("%.1f g", servingGrams)},
						{"calories/100g", fmt.Sprintf("%.1f", entry.CaloriesPer100g)},
						{"protein/100g", fmt.Sprintf("%.2f g", entry.ProteinPer100g)},
						{"carbs/100g", fmt.Sprintf("%.2f g", entry.CarbsPer100g)},
						{"fat/100g", fmt.Sprintf("%.2f g", entry.FatPer100g)},
						{"fiber/100g", fmt.Sprintf("%.2f g", entry.FiberPer100g)},
						{"source", source},
					},
				}
			})
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item to record nutrition for (required)")
	cmd.Flags().Float64Var(&servingGrams, "serving-grams", 0, "gram weight of one label serving, so the values below can be stored per 100g")
	cmd.Flags().Float64Var(&calories, "calories", 0, "calories per label serving")
	cmd.Flags().Float64Var(&protein, "protein", 0, "protein grams per label serving")
	cmd.Flags().Float64Var(&carbs, "carbs", 0, "carb grams per label serving")
	cmd.Flags().Float64Var(&fat, "fat", 0, "fat grams per label serving")
	cmd.Flags().Float64Var(&fiber, "fiber", 0, "fiber grams per label serving")
	cmd.Flags().StringVar(&note, "note", "", "free-text note, e.g. the exact product name the label came from")
	cmd.Flags().BoolVar(&patrickProvided, "patrick-provided", false, "these numbers came from Patrick rather than being read off a package")
	cmd.Flags().BoolVar(&estimated, "estimated", false, "these numbers are a reasonable estimate (a seasoning blend USDA has no entry for), not read off a package")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove this item's label nutrition, deferring to its USDA link again")
	return cmd
}

func newPantrySetShelfLifeCmd() *cobra.Command {
	var itemID int64
	var days int
	var estimated bool

	cmd := &cobra.Command{
		Use:   "set-shelf-life",
		Short: "Set a typical shelf-life estimate for an item, used to auto-set expiration dates on new stock",
		RunE: func(cmd *cobra.Command, args []string) error {
			if days <= 0 {
				return fmt.Errorf("--days must be positive")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			item, err := store.GetPantryItem(db, itemID)
			if err != nil {
				return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
			}
			source := "patrick_provided"
			if estimated {
				source = "estimated"
			}
			if err := store.SetShelfLife(db, itemID, days, source); err != nil {
				return fmt.Errorf("saving shelf life: %w", err)
			}
			return renderResult(
				map[string]any{"item_id": itemID, "item": item.Name, "days": days, "source": source},
				"Set typical shelf life for %s: %d days (%s)\n", item.Name, days, source)
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item id (required)")
	cmd.MarkFlagRequired("item-id")
	cmd.Flags().IntVar(&days, "days", 0, "typical days from purchase until this item spoils/expires (required)")
	cmd.MarkFlagRequired("days")
	cmd.Flags().BoolVar(&estimated, "estimated", false, "mark this as Claude's estimate rather than a value Patrick provided directly")
	return cmd
}

func newPantryLinkNutritionCmd() *cobra.Command {
	var itemID int64
	var all, force, clear bool
	var query string
	var fdcID int

	cmd := &cobra.Command{
		Use:   "link-nutrition",
		Short: "Link pantry item(s) to real USDA nutrition data, for computed macros",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == cmd.Flags().Changed("item-id") {
				return fmt.Errorf("specify exactly one of --item-id or --all")
			}
			if clear && (all || query != "" || force) {
				return fmt.Errorf("--clear only works with a single --item-id, not combined with --all/--query/--force")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if clear {
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
				}
				if err := store.ClearPantryItemFDCID(db, itemID); err != nil {
					return fmt.Errorf("clearing link: %w", err)
				}
				return renderResult(
					map[string]any{"item_id": itemID, "item": item.Name, "cleared": true},
					"Cleared nutrition link for %s\n", item.Name)
			}

			// An explicit fdc_id skips the search entirely. The search path
			// deliberately only considers generic Foundation/SR Legacy
			// entries and filters branded ones out, which is right when
			// guessing from an item's name - but a branded pantry item
			// often has a branded USDA entry that is the correct answer,
			// and without this there was no way to say so.
			if fdcID != 0 {
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
				}
				cached, ok, err := store.GetNutritionByFDCID(db, fdcID)
				if err != nil {
					return fmt.Errorf("looking up fdc_id %d: %w", fdcID, err)
				}
				if !ok {
					return fmt.Errorf("fdc_id %d isn't in the nutrition cache yet - log something with `meal log --grams` first, or link by name and correct it", fdcID)
				}
				if err := store.SetPantryItemFDCID(db, itemID, fdcID); err != nil {
					return fmt.Errorf("linking: %w", err)
				}
				return renderResult(
					map[string]any{"item_id": itemID, "item": item.Name, "fdc_id": fdcID, "description": cached.Description},
					"Linked %s -> %s (fdc_id %d)\n", item.Name, cached.Description, fdcID)
			}

			usdaClient, err := usdaClientFromConfig()
			if err != nil {
				return fmt.Errorf("loading USDA API key: %w", err)
			}
			if usdaClient == nil {
				return fmt.Errorf("no USDA API key configured (see `meal doctor`)")
			}

			var items []store.PantryItem
			if all {
				items, err = store.ListPantryItemsMissingFDCID(db)
				if err != nil {
					return fmt.Errorf("listing unlinked items: %w", err)
				}
			} else {
				item, err := store.GetPantryItem(db, itemID)
				if err != nil {
					return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
				}
				items = []store.PantryItem{item}
			}

			var linked, skipped, failed int
			for _, item := range items {
				ok, desc, err := domain.LinkPantryItemNutrition(cmd.Context(), db, usdaClient, item.ID, query, force)
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "warning: linking %s: %v\n", item.Name, err)
					continue
				}
				if !ok {
					skipped++
					fmt.Fprintf(os.Stderr, "no USDA match found for %s\n", item.Name)
					continue
				}
				linked++
				fmt.Printf("Linked %s -> %s\n", item.Name, desc)
			}

			return output.Render(os.Stdout, jsonOutput, map[string]int{"linked": linked, "skipped": skipped, "failed": failed}, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"linked", fmt.Sprintf("%d", linked)},
						{"no match found", fmt.Sprintf("%d", skipped)},
						{"errors", fmt.Sprintf("%d", failed)},
					},
				}
			})
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "link this one pantry item (mutually exclusive with --all)")
	cmd.Flags().BoolVar(&all, "all", false, "link every pantry item that doesn't have a USDA match yet")
	cmd.Flags().StringVar(&query, "query", "", "search using this text instead of the item's own name (for correcting a bad match)")
	cmd.Flags().BoolVar(&force, "force", false, "re-link an item that's already linked (default: skip items already linked)")
	cmd.Flags().IntVar(&fdcID, "fdc-id", 0, "link to this exact already-cached USDA food instead of searching (for a branded item the name search filters out)")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear a bad link instead of searching (single --item-id only)")
	return cmd
}

// newPantryMergeCmd folds a duplicate pantry item into the one that should
// survive. Duplicates arise when the same real product gets catalogued twice
// under different names (e.g. "Heavy Cream" and "Whipping Cream"), which
// silently splits stock and recipe references between two rows.
func newPantryMergeCmd() *cobra.Command {
	var fromID, intoID int64
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Fold a duplicate pantry item into another, repointing stock, recipes, purchases and aliases",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			res, err := store.MergePantryItems(db, fromID, intoID)
			if err != nil {
				return err
			}
			rows := [][]string{
				{"merged away", fmt.Sprintf("%s (id %d)", res.FromName, res.FromID)},
				{"kept", fmt.Sprintf("%s (id %d)", res.IntoName, res.IntoID)},
				{"stock lots moved", fmt.Sprintf("%d", res.StockLots)},
				{"purchase lines moved", fmt.Sprintf("%d", res.PurchaseItems)},
				{"recipe lines moved", fmt.Sprintf("%d", res.RecipeLines)},
				{"cook-event lines moved", fmt.Sprintf("%d", res.CookEventLines)},
				{"waste rows moved", fmt.Sprintf("%d", res.WasteRows)},
				{"aliases moved", fmt.Sprintf("%d", res.Aliases)},
				{"conversions moved", fmt.Sprintf("%d", res.ConversionsMoved)},
			}
			if res.ShelfLifeDropped {
				rows = append(rows, []string{"shelf life", "duplicate dropped, kept the surviving item's"})
			}
			if err := output.Render(os.Stdout, jsonOutput, res, func() output.Table {
				return output.Table{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
			}); err != nil {
				return err
			}
			if !jsonOutput {
				fmt.Println("\nStock lots keep their original units - run `meal pantry list` and reconcile if the two items tracked different units.")
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&fromID, "from", 0, "pantry item id to merge away (it is deleted)")
	cmd.Flags().Int64Var(&intoID, "into", 0, "pantry item id to keep")
	cmd.MarkFlagRequired("from")
	cmd.MarkFlagRequired("into")
	return cmd
}

// newPantrySetDefaultUnitCmd changes the unit an item is tracked in going
// forward. default_unit is what `shop add-item` and `pantry adjust` fall back
// to when no --unit is given, so an item whose default disagrees with the unit
// its recipes reference quietly accumulates stock that cook can never touch.
func newPantrySetDefaultUnitCmd() *cobra.Command {
	var itemID int64
	var unit string
	cmd := &cobra.Command{
		Use:   "set-default-unit",
		Short: "Change the unit an item is tracked in from now on (does not convert existing stock)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			item, err := store.GetPantryItem(db, itemID)
			if err != nil {
				return fmt.Errorf("item %d not found (try `meal pantry list --all`): %w", itemID, err)
			}
			if item.DefaultUnit == unit {
				fmt.Printf("%s already tracked in %s, nothing to do\n", item.Name, unit)
				return nil
			}
			if _, err := db.Exec(`UPDATE pantry_items SET default_unit = ? WHERE id = ?`, unit, itemID); err != nil {
				return fmt.Errorf("updating default unit: %w", err)
			}
			fmt.Printf("%s: default unit %s -> %s\n", item.Name, item.DefaultUnit, unit)

			var lots int
			var qty float64
			if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(quantity),0) FROM pantry_stock
				WHERE item_id = ? AND status = 'on_hand' AND quantity > 0 AND unit <> ?`, itemID, unit).Scan(&lots, &qty); err != nil {
				return err
			}
			if lots > 0 {
				fmt.Printf("warning: %d existing lot(s) totalling %.2f are still in the old unit and will not be matched.\n", lots, qty)
				fmt.Printf("  Convert them with: meal pantry adjust --item-id %d --qty -<old> --unit <old-unit> --reason \"unit reconciliation\"\n", itemID)
				fmt.Printf("                     meal pantry adjust --item-id %d --qty +<new> --unit %s --reason \"unit reconciliation\"\n", itemID, unit)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "pantry item id")
	cmd.Flags().StringVar(&unit, "unit", "", "the unit this item should be tracked in from now on")
	cmd.MarkFlagRequired("item-id")
	cmd.MarkFlagRequired("unit")
	return cmd
}

// newPantryBackfillExpiryCmd fills in expiration dates on lots that predate
// their item's shelf-life estimate.
//
// shop finish and pantry adjust only stamp expires_date at the moment stock is
// created, using whatever shelf life exists then. Setting a shelf life
// afterwards leaves every existing lot with a NULL expiry, which quietly
// removes it from `pantry list --expiring-within` and from the dashboard's
// expiring-soon panel - the exact stock most likely to be wasted.
func newPantryBackfillExpiryCmd() *cobra.Command {
	var dryRun, recompute bool
	cmd := &cobra.Command{
		Use:   "backfill-expiry",
		Short: "Set expiration dates on on-hand lots from their item's shelf-life estimate",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			const sel = `
				SELECT ps.id, pi.name, ps.quantity, ps.unit, ps.acquired_date,
				       date(ps.acquired_date, '+' || t.days || ' days')
				FROM pantry_stock ps
				JOIN pantry_items pi ON pi.id = ps.item_id
				JOIN typical_shelf_life t ON t.item_id = ps.item_id
				WHERE ps.status = 'on_hand' AND ps.quantity > 0
				  AND (ps.expires_date IS NULL OR (? AND ps.expires_date <> date(ps.acquired_date, '+' || t.days || ' days')))
				ORDER BY date(ps.acquired_date, '+' || t.days || ' days')`
			rows, err := db.Query(sel, recompute)
			if err != nil {
				return err
			}
			type lot struct {
				id                            int64
				name, unit, acquired, expires string
				qty                           float64
			}
			var lots []lot
			for rows.Next() {
				var l lot
				if err := rows.Scan(&l.id, &l.name, &l.qty, &l.unit, &l.acquired, &l.expires); err != nil {
					rows.Close()
					return err
				}
				lots = append(lots, l)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(lots) == 0 {
				if recompute {
					fmt.Println("Nothing to do: every on-hand lot already matches its item's current shelf-life estimate.")
				} else {
					fmt.Println("Nothing to backfill: every on-hand lot with a shelf-life estimate already has an expiration date.")
					fmt.Println("Use --recompute to also restamp lots whose shelf-life estimate has since changed.")
				}
				return nil
			}

			var out [][]string
			for _, l := range lots {
				out = append(out, []string{
					fmt.Sprintf("%d", l.id), l.name,
					fmt.Sprintf("%.2f %s", l.qty, l.unit), l.acquired, l.expires,
				})
			}
			if err := output.Render(os.Stdout, jsonOutput, lots, func() output.Table {
				return output.Table{Headers: []string{"LOT", "ITEM", "QTY", "ACQUIRED", "EXPIRES"}, Rows: out}
			}); err != nil {
				return err
			}

			if dryRun {
				fmt.Printf("\n%d lot(s) would be updated (--dry-run, nothing written).\n", len(lots))
				return nil
			}
			res, err := db.Exec(`
				UPDATE pantry_stock SET expires_date = (
					SELECT date(pantry_stock.acquired_date, '+' || t.days || ' days')
					FROM typical_shelf_life t WHERE t.item_id = pantry_stock.item_id)
				WHERE status = 'on_hand' AND quantity > 0
				  AND item_id IN (SELECT item_id FROM typical_shelf_life)
				  AND (expires_date IS NULL OR (? AND expires_date <> (
					SELECT date(pantry_stock.acquired_date, '+' || t.days || ' days')
					FROM typical_shelf_life t WHERE t.item_id = pantry_stock.item_id)))`, recompute)
			if err != nil {
				return fmt.Errorf("backfilling expiry dates: %w", err)
			}
			n, _ := res.RowsAffected()
			fmt.Printf("\nBackfilled %d lot(s).\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing")
	cmd.Flags().BoolVar(&recompute, "recompute", false, "also restamp lots that already have an expiry, for when the item's shelf-life estimate has changed")
	return cmd
}

// newPantrySetLotPriceCmd corrects the unit cost recorded against one stock
// lot, identified by the lot id shown in `pantry list --expiring-within` and
// in the backfill-expiry listing.
//
// Two situations need this and neither had a fix short of editing the
// database: a lot created by `pantry adjust` without --price (so it carries no
// cost at all), and a seeded lot that stored a package's line total where its
// unit price belonged, which silently multiplies every downstream meal cost.
func newPantrySetLotPriceCmd() *cobra.Command {
	var lotID int64
	var price float64
	cmd := &cobra.Command{
		Use:   "set-lot-price",
		Short: "Set or correct the unit cost on one pantry stock lot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if price < 0 {
				return fmt.Errorf("--price cannot be negative")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var name, unit string
			var qty float64
			var old sql.NullFloat64
			err = db.QueryRow(`
				SELECT pi.name, ps.quantity, ps.unit, ps.unit_cost
				FROM pantry_stock ps JOIN pantry_items pi ON pi.id = ps.item_id
				WHERE ps.id = ?`, lotID).Scan(&name, &qty, &unit, &old)
			if err == sql.ErrNoRows {
				return fmt.Errorf("no pantry stock lot with id %d", lotID)
			}
			if err != nil {
				return err
			}
			if _, err := db.Exec(`UPDATE pantry_stock SET unit_cost = ? WHERE id = ?`, price, lotID); err != nil {
				return fmt.Errorf("updating lot %d: %w", lotID, err)
			}
			prev := "(none)"
			if old.Valid {
				prev = fmt.Sprintf("$%.4f", old.Float64)
			}
			fmt.Printf("lot %d  %s  %.3f %s: unit cost %s -> $%.4f (lot value $%.2f)\n",
				lotID, name, qty, unit, prev, price, qty*price)
			return nil
		},
	}
	cmd.Flags().Int64Var(&lotID, "lot-id", 0, "pantry stock lot id")
	cmd.Flags().Float64Var(&price, "price", 0, "unit cost for this lot, per its own unit")
	cmd.MarkFlagRequired("lot-id")
	cmd.MarkFlagRequired("price")
	return cmd
}

// newPantryMoveLotCmd repoints one stock lot to a different pantry item -
// the fix for a receipt line matched to the wrong item, once `shop finish`
// has already turned it into stock.
func newPantryMoveLotCmd() *cobra.Command {
	var stockID, toItem int64
	cmd := &cobra.Command{
		Use:   "move-lot",
		Short: "Move one on-hand stock lot to a different pantry item, keeping its price, dates and quantity",
		RunE: func(cmd *cobra.Command, args []string) error {
			if stockID == 0 || toItem == 0 {
				return fmt.Errorf("--stock-id and --to-item are both required")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			m, err := store.MoveStockLot(db, stockID, toItem)
			if err != nil {
				return err
			}
			if err := output.Render(os.Stdout, jsonOutput, m, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"lot", fmt.Sprintf("%d", m.StockID)},
						{"quantity", fmt.Sprintf("%.3f %s", m.Quantity, m.Unit)},
						{"moved from", fmt.Sprintf("%s (item %d)", m.FromItemName, m.FromItemID)},
						{"moved to", fmt.Sprintf("%s (item %d)", m.ToItemName, m.ToItemID)},
					},
				}
			}); err != nil {
				return err
			}
			if !jsonOutput && m.UnitMismatch != "" {
				fmt.Printf("\nNOTE: this lot is in %s but %s is tracked in %s.\n", m.Unit, m.ToItemName, m.UnitMismatch)
				fmt.Printf("Record a bridge with `meal pantry set-conversion --item-id %d --from %s --to %s --factor N`,\n", m.ToItemID, m.UnitMismatch, m.Unit)
				fmt.Printf("or change the item with `meal pantry set-default-unit --item-id %d --unit %s`.\n", m.ToItemID, m.Unit)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&stockID, "stock-id", 0, "pantry stock lot id to move (see `meal pantry list --expiring-within`)")
	cmd.Flags().Int64Var(&toItem, "to-item", 0, "pantry item id the lot should belong to")
	return cmd
}
