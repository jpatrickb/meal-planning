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

type cookResult struct {
	CookEventID         int64    `json:"cook_event_id"`
	RecipeID            string   `json:"recipe_id"`
	CookedDate          string   `json:"cooked_date"`
	ServingsYielded     float64  `json:"servings_yielded"`
	ServingsRemaining   float64  `json:"servings_remaining"`
	TotalCost           float64  `json:"total_cost"`
	ConsumptionLogged   int      `json:"consumption_entries_logged"`
	IngredientsConsumed int      `json:"ingredients_consumed"`
	PantryShortfalls    []string `json:"pantry_shortfalls,omitempty"`
}

func newCookCmd() *cobra.Command {
	var date string
	var servesOverride int
	var costOverride float64
	var eatenNow string
	var mealSlot string
	var scale float64
	var ingredientQty string
	var acceptZeroStock string

	cmd := &cobra.Command{
		Use:   "cook <slug>",
		Short: "Log cooking a recipe, creating a leftover ledger entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			if date == "" {
				date = todayDate()
			}
			slotPtr, err := validateMealSlot(mealSlot)
			if err != nil {
				return err
			}
			eaten, err := parsePersonAmounts(eatenNow)
			if err != nil {
				return fmt.Errorf("--eaten-now: %w", err)
			}
			if scale <= 0 {
				return fmt.Errorf("--scale must be positive")
			}
			ingredientOverrides, err := parseItemQtyOverrides(ingredientQty)
			if err != nil {
				return fmt.Errorf("--ingredient-qty: %w", err)
			}
			acceptZeroStockSet, err := parseItemIDSet(acceptZeroStock)
			if err != nil {
				return fmt.Errorf("--accept-zero-stock: %w", err)
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			recipe, err := store.GetRecipe(db, slug)
			if err != nil {
				return fmt.Errorf("recipe %q not found (try `meal recipe list` or `meal sync recipes`): %w", slug, err)
			}
			meta, err := store.GetRecipeMeta(db, slug)
			if err != nil {
				return fmt.Errorf("loading recipe metadata: %w", err)
			}

			var invocationOverride *int
			if cmd.Flags().Changed("serves-override") {
				invocationOverride = &servesOverride
			}
			servings, err := domain.ResolveServings(invocationOverride, meta.ServesOverride, recipe.ServesRaw, slug)
			if err != nil {
				return err
			}

			costRes, err := domain.ResolveRecipeCost(db, slug, meta)
			if err != nil {
				return fmt.Errorf("resolving cost: %w", err)
			}
			totalCost := costRes.Cost
			costKnown := costRes.Source != "unknown"
			if cmd.Flags().Changed("cost") {
				totalCost = costOverride
				costKnown = true
			}
			if !costKnown {
				if len(costRes.UnknownItems) > 0 {
					fmt.Fprintf(os.Stderr, "warning: cost unknown for %s (no purchase history yet for: %s), logging $0 (or set with `meal recipe set-meta %s --cost X`)\n",
						slug, strings.Join(costRes.UnknownItems, ", "), slug)
				} else {
					fmt.Fprintf(os.Stderr, "warning: no cost set for %s, logging $0 (set with `meal recipe set-meta %s --cost X`)\n", slug, slug)
				}
			}

			macroRes, err := domain.ResolveRecipeMacros(db, slug, meta, float64(servings))
			if err != nil {
				return fmt.Errorf("resolving macros: %w", err)
			}

			var totalEatenNow float64
			for _, pa := range eaten {
				totalEatenNow += pa.Servings
			}
			if totalEatenNow > float64(servings)+1e-9 {
				return fmt.Errorf("--eaten-now totals %.2f servings but the recipe only yields %d", totalEatenNow, servings)
			}

			tx, err := db.Begin()
			if err != nil {
				return fmt.Errorf("starting transaction: %w", err)
			}
			defer tx.Rollback()

			cookEventID, err := store.InsertCookEvent(tx, store.CookEvent{
				RecipeID: slug, CookedDate: date, ServingsYielded: float64(servings), TotalCost: totalCost,
			})
			if err != nil {
				return fmt.Errorf("recording cook event: %w", err)
			}

			// Pantry decrement: for every mapped ingredient (not_tracked ones
			// never touch the pantry, by design), consume the recipe's
			// quantity scaled by --scale, or an explicit --ingredient-qty
			// override for that item. Insufficient stock fails the whole
			// cook (rolled back) unless that item is explicitly named in
			// --accept-zero-stock -- docs §2: proceeding on a known-stale
			// pantry record is a per-item decision, never a silent default.
			ingredientRows, err := store.ListRecipeIngredients(db, slug)
			if err != nil {
				return fmt.Errorf("loading ingredient mapping: %w", err)
			}
			var ingredientsConsumed int
			var pantryShortfalls []string
			consumedByItem := map[int64]struct {
				Qty  float64
				Unit string
			}{}
			for _, ri := range ingredientRows {
				if ri.Status != "mapped" {
					continue
				}
				qty := *ri.Quantity * scale
				source := "recipe_default"
				if override, ok := ingredientOverrides[*ri.ItemID]; ok {
					qty = override
					source = "override"
				}
				if qty == 0 {
					continue
				}

				accepted := acceptZeroStockSet[*ri.ItemID]
				lots, err := decrementPantryFIFOOrHint(db, tx, *ri.ItemID, *ri.Unit, qty, accepted)
				if err != nil {
					return err
				}
				if accepted {
					var got float64
					for _, l := range lots {
						got += l.QuantityAsked
					}
					if got+1e-9 < qty {
						item, _ := store.GetPantryItem(db, *ri.ItemID)
						pantryShortfalls = append(pantryShortfalls, fmt.Sprintf("%s: needed %.3f %s, only %.3f on hand", item.Name, qty, *ri.Unit, got))
					}
				}
				for _, l := range lots {
					existing, seen := consumedByItem[*ri.ItemID]
					add := l.Quantity
					if seen && domain.CanonicalUnit(l.Unit) != domain.CanonicalUnit(existing.Unit) {
						converted, ok, cerr := domain.ConvertQuantity(db, ri.ItemID, l.Quantity, l.Unit, existing.Unit)
						if cerr != nil {
							return fmt.Errorf("converting consumed quantity: %w", cerr)
						}
						if !ok {
							add = 0
						} else {
							add = converted
						}
					}
					if !seen {
						consumedByItem[*ri.ItemID] = struct {
							Qty  float64
							Unit string
						}{Qty: l.Quantity, Unit: l.Unit}
					} else {
						existing.Qty += add
						consumedByItem[*ri.ItemID] = existing
					}
					// The lot's own unit, not the recipe's: those differ
					// whenever a conversion reached stock recorded some
					// other way, and the quantity debited is in the lot's.
					if _, err := store.InsertCookEventIngredient(tx, store.CookEventIngredient{
						CookEventID: cookEventID, ItemID: *ri.ItemID, PantryStockID: l.StockID,
						Quantity: l.Quantity, Unit: l.Unit, Source: source,
					}); err != nil {
						return fmt.Errorf("recording ingredient consumption: %w", err)
					}
				}
				ingredientsConsumed++
			}

			// What this cook actually consumed, divided by what it actually
			// yielded, beats the recipe in the abstract: a scaled batch, an
			// overridden ingredient, or a serving count unlike the recipe's
			// all make the two disagree. A manual value still wins, and the
			// recipe resolution remains the fallback.
			if meta.MacrosPerServing == nil && len(consumedByItem) > 0 {
				perServing, macroSrc, gaps, err := domain.PerServingFromConsumption(db, slug, consumedByItem, float64(servings))
				if err != nil {
					return fmt.Errorf("computing macros from what was consumed: %w", err)
				}
				if len(gaps) == 0 {
					macroRes = domain.RecipeMacroResolution{PerServing: perServing, Source: "computed"}
					if macroSrc == "estimated" {
						macroRes.Source = "computed_estimated"
					}
				}
			}

			var insertedIDs []int64
			if len(eaten) > 0 {
				warnIfNoMacros(slug, macroRes)
				costPerServing := 0.0
				if servings > 0 {
					costPerServing = totalCost / float64(servings)
				}
				for _, pa := range eaten {
					entry := store.ConsumptionEntry{
						ConsumedDate: date, Person: pa.Person, MealSlot: slotPtr, Source: "cooked",
						RecipeID: &slug, CookEventID: &cookEventID, Servings: pa.Servings,
						Cost: costPerServing * pa.Servings,
					}
					applyRecipeMacros(&entry, macroRes, pa.Servings)
					id, err := store.InsertConsumptionEntry(tx, entry)
					if err != nil {
						return fmt.Errorf("logging consumption for %s: %w", pa.Person, err)
					}
					insertedIDs = append(insertedIDs, id)
				}
				if len(insertedIDs) > 1 {
					if err := store.SetConsumptionEventGroup(tx, insertedIDs, insertedIDs[0]); err != nil {
						return fmt.Errorf("linking shared-meal entries: %w", err)
					}
				}
				if err := store.DecrementCookEventServings(tx, cookEventID, totalEatenNow); err != nil {
					return fmt.Errorf("decrementing servings: %w", err)
				}
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing: %w", err)
			}

			result := cookResult{
				CookEventID: cookEventID, RecipeID: slug, CookedDate: date,
				ServingsYielded: float64(servings), ServingsRemaining: float64(servings) - totalEatenNow,
				TotalCost: totalCost, ConsumptionLogged: len(insertedIDs),
				IngredientsConsumed: ingredientsConsumed, PantryShortfalls: pantryShortfalls,
			}
			for _, s := range pantryShortfalls {
				fmt.Fprintf(os.Stderr, "warning: pantry shortfall accepted, %s\n", s)
			}
			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"cook event id", fmt.Sprintf("%d", result.CookEventID)},
						{"recipe", result.RecipeID},
						{"cooked date", result.CookedDate},
						{"servings yielded", fmt.Sprintf("%.2f", result.ServingsYielded)},
						{"servings remaining", fmt.Sprintf("%.2f", result.ServingsRemaining)},
						{"total cost", fmt.Sprintf("$%.2f", result.TotalCost)},
						{"consumption entries logged", fmt.Sprintf("%d", result.ConsumptionLogged)},
						{"pantry ingredients consumed", fmt.Sprintf("%d", result.IngredientsConsumed)},
					},
				}
			})
		},
	}

	cmd.Flags().StringVar(&date, "date", "", "date cooked, YYYY-MM-DD (default: today)")
	cmd.Flags().IntVar(&servesOverride, "serves-override", 0, "override this cook's serving count (highest precedence)")
	cmd.Flags().Float64Var(&costOverride, "cost", 0, "override the recipe's saved total cost for this cook event")
	cmd.Flags().StringVar(&eatenNow, "eaten-now", "", "servings eaten immediately, e.g. patrick=1,thea=1")
	cmd.Flags().StringVar(&mealSlot, "meal-slot", "", "meal slot for any --eaten-now entries: breakfast, lunch, dinner, snack")
	cmd.Flags().Float64Var(&scale, "scale", 1.0, "scale factor applied to every mapped ingredient's default quantity (e.g. 2.0 for a doubled batch)")
	cmd.Flags().StringVar(&ingredientQty, "ingredient-qty", "", "override specific ingredient quantities for this cook, e.g. 5=0.5,12=2 (pantry item id=quantity)")
	cmd.Flags().StringVar(&acceptZeroStock, "accept-zero-stock", "", "pantry item ids to consume from even if on-hand stock is less than needed (or zero), comma-separated")
	cmd.AddCommand(newCookVoidCmd())
	return cmd
}

func newCookVoidCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "void <cook-event-id>",
		Short: "Reverse an erroneous/duplicate cook event: restores its pantry decrements, deletes its consumption-log rows, and removes it",
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

			result, err := store.VoidCookEvent(db, id)
			if err != nil {
				return fmt.Errorf("voiding cook event %d: %w", id, err)
			}

			return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"cook event id", fmt.Sprintf("%d", result.CookEventID)},
						{"recipe", result.RecipeID},
						{"servings yielded (voided)", fmt.Sprintf("%.2f", result.ServingsYielded)},
						{"pantry lots restored", fmt.Sprintf("%d", result.PantryLotsRestored)},
						{"consumption entries deleted", fmt.Sprintf("%d", result.ConsumptionEntriesDeleted)},
					},
				}
			})
		},
	}
	return cmd
}
