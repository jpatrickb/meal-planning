package cli

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/config"
	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
	"github.com/jpatrickb/meal-planning/internal/usda"
)

var validAdHocSources = map[string]bool{"purchased_ready": true, "eaten_out": true, "from_pantry": true}
var validMacroSources = map[string]bool{"local_meta": true, "usda": true, "estimated": true}

type logResult struct {
	ConsumptionID     int64                `json:"consumption_id"`
	ConsumedDate      string               `json:"consumed_date"`
	Person            string               `json:"person"`
	Source            string               `json:"source"`
	RecipeID          *string              `json:"recipe_id,omitempty"`
	CookEventID       *int64               `json:"cook_event_id,omitempty"`
	Servings          float64              `json:"servings"`
	Cost              float64              `json:"cost"`
	ServingsRemaining *float64             `json:"cook_event_servings_remaining,omitempty"`
	PantryDepleted    []pantryDepletedItem `json:"pantry_depleted,omitempty"`
}

type pantryDepletedItem struct {
	ItemID    int64   `json:"item_id"`
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	Unit      string  `json:"unit"`
	CostKnown bool    `json:"cost_known"`
}

func newLogCmd() *cobra.Command {
	var person, mealSlot, date, fromLeftovers, source, macroSource, depletePantry, acceptZeroStock string
	var servings, cost, calories, protein, carbs, fat, fiber, grams float64
	var reuseFDCID int
	var forceFreshLookup bool

	cmd := &cobra.Command{
		Use:   "log <food-or-label>",
		Short: "Log a consumption event: leftovers, or ad-hoc food",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			if err := validatePerson(person); err != nil {
				return err
			}
			slotPtr, err := validateMealSlot(mealSlot)
			if err != nil {
				return err
			}
			if date == "" {
				date = todayDate()
			}
			if servings <= 0 {
				servings = 1
			}
			depletions, err := parsePantryDepletions(depletePantry)
			if err != nil {
				return fmt.Errorf("--deplete-pantry: %w", err)
			}
			acceptZeroStockSet, err := parseItemIDSet(acceptZeroStock)
			if err != nil {
				return fmt.Errorf("--accept-zero-stock: %w", err)
			}
			if len(depletions) > 0 && fromLeftovers != "" {
				return fmt.Errorf("--deplete-pantry and --from-leftovers are mutually exclusive")
			}
			if len(depletions) > 0 {
				if cmd.Flags().Changed("source") && source != "from_pantry" {
					return fmt.Errorf("--deplete-pantry implies --source from_pantry, not %q", source)
				}
				source = "from_pantry"
			} else if source == "from_pantry" {
				return fmt.Errorf("--source from_pantry requires --deplete-pantry naming what was consumed")
			}

			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if fromLeftovers != "" {
				return logFromLeftovers(db, label, person, slotPtr, date, servings, fromLeftovers)
			}

			if !validAdHocSources[source] {
				return fmt.Errorf("invalid --source %q: must be purchased_ready, eaten_out, or from_pantry", source)
			}

			entry := store.ConsumptionEntry{
				ConsumedDate: date, Person: person, MealSlot: slotPtr, Source: source,
				FreeTextFood: &label, Servings: servings, Cost: cost,
			}
			macrosGiven := cmd.Flags().Changed("calories") || cmd.Flags().Changed("protein") ||
				cmd.Flags().Changed("carbs") || cmd.Flags().Changed("fat") || cmd.Flags().Changed("fiber")
			if macrosGiven {
				if !validMacroSources[macroSource] {
					return fmt.Errorf("invalid --macro-source %q: must be local_meta, usda, or estimated", macroSource)
				}
				entry.Calories, entry.Protein, entry.Carbs, entry.Fat, entry.Fiber = &calories, &protein, &carbs, &fat, &fiber
				entry.MacroSource = &macroSource
			} else if grams > 0 {
				if reuseFDCID != 0 && forceFreshLookup {
					return fmt.Errorf("--reuse-fdc-id and --force-fresh-lookup are mutually exclusive")
				}

				var resolved domain.ResolvedMacros
				switch {
				case reuseFDCID != 0:
					resolved, err = domain.ResolveFoodMacrosByFDCID(db, reuseFDCID, label)
					if err != nil {
						return fmt.Errorf("reusing fdc_id %d: %w", reuseFDCID, err)
					}
				case forceFreshLookup:
					resolved, err = forceFreshMacrosFor(cmd, db, label)
					if err != nil {
						return fmt.Errorf("resolving nutrition: %w", err)
					}
				default:
					outcome, err := resolveMacrosFor(cmd, db, label)
					if err != nil {
						return fmt.Errorf("resolving nutrition: %w", err)
					}
					if outcome.Resolved == nil {
						return renderNutritionCandidates(label, outcome.Candidates)
					}
					resolved = *outcome.Resolved
				}

				scale := grams / 100
				cal, pro, carb, fatV, fib := resolved.CaloriesPer100g*scale, resolved.ProteinPer100g*scale,
					resolved.CarbsPer100g*scale, resolved.FatPer100g*scale, resolved.FiberPer100g*scale
				entry.Calories, entry.Protein, entry.Carbs, entry.Fat, entry.Fiber = &cal, &pro, &carb, &fatV, &fib
				src := resolved.Source
				entry.MacroSource = &src
				fmt.Fprintf(os.Stderr, "nutrition: %s (%s), %.0fg -> %.0f cal, %.1fg protein\n", resolved.Description, resolved.Source, grams, cal, pro)
			}

			// A pantry depletion names exactly what was eaten, so it can
			// answer the macro question itself - no USDA round trip, no
			// guessing at a gram weight for the whole dish. Explicit macro
			// flags and --grams still win, since those are a deliberate
			// statement about this particular entry.
			// Validate every named item before anything else touches them,
			// so a typo'd id fails with the item id and a pointer to the
			// catalog rather than a bare "sql: no rows in result set" from
			// whichever lookup happened to run first.
			depletionUnits := map[int64]string{}
			for _, d := range depletions {
				item, err := store.GetPantryItem(db, d.ItemID)
				if err != nil {
					return fmt.Errorf("pantry item %d not found (try `meal pantry list --all`): %w", d.ItemID, err)
				}
				unit := d.Unit
				if unit == "" {
					unit = item.DefaultUnit
				}
				depletionUnits[d.ItemID] = unit
			}

			if len(depletions) > 0 && !macrosGiven && grams <= 0 {
				byItem := map[int64]struct {
					Qty  float64
					Unit string
				}{}
				for _, d := range depletions {
					unit := depletionUnits[d.ItemID]
					byItem[d.ItemID] = struct {
						Qty  float64
						Unit string
					}{Qty: d.Qty, Unit: unit}
				}
				computed, macroSrc, unknownItems, err := domain.DepletedItemMacros(db, byItem)
				if err != nil {
					return fmt.Errorf("computing macros from the pantry items: %w", err)
				}
				if len(unknownItems) > 0 {
					fmt.Fprintf(os.Stderr, "warning: macros incomplete for this entry (%s)\n", strings.Join(unknownItems, "; "))
				}
				if computed.Calories > 0 {
					cal, pro, carb, fatV, fib := computed.Calories, computed.Protein, computed.Carbs, computed.Fat, computed.Fiber
					entry.Calories, entry.Protein, entry.Carbs, entry.Fat, entry.Fiber = &cal, &pro, &carb, &fatV, &fib
					entry.MacroSource = &macroSrc
					fmt.Fprintf(os.Stderr, "nutrition: computed from the depleted pantry items -> %.0f cal, %.1fg protein\n", cal, pro)
				}
			}

			tx, err := db.Begin()
			if err != nil {
				return fmt.Errorf("starting transaction: %w", err)
			}
			defer tx.Rollback()

			// Pantry decrement: consume real on-hand stock for each named
			// item, same FIFO-with-hint behavior as `meal cook`'s ingredient
			// consumption. Cost defaults to the sum of what was actually
			// decremented (unknown for any item with no purchase history yet)
			// unless --cost was given explicitly, which always wins.
			var depleted []pantryDepletedItem
			var drawnLots []store.ConsumptionDepletion
			var depletedCost float64
			var anyCostUnknown bool
			for _, d := range depletions {
				item, err := store.GetPantryItem(db, d.ItemID)
				if err != nil {
					return fmt.Errorf("pantry item %d not found (try `meal pantry list --all`): %w", d.ItemID, err)
				}
				unit := depletionUnits[d.ItemID]
				lots, err := decrementPantryFIFOOrHint(db, tx, d.ItemID, unit, d.Qty, acceptZeroStockSet[d.ItemID])
				if err != nil {
					return err
				}
				var got, itemCost float64
				var costKnown bool
				for _, l := range lots {
					// got is compared against the requested quantity, so it
					// has to be in the requested unit; cost is priced per
					// the lot's own unit.
					got += l.QuantityAsked
					if l.UnitCost != nil {
						itemCost += *l.UnitCost * l.Quantity
						costKnown = true
					}
				}
				if got+1e-9 < d.Qty && !acceptZeroStockSet[d.ItemID] {
					return fmt.Errorf("insufficient stock for %s: needed %.3f %s, only %.3f on hand", item.Name, d.Qty, unit, got)
				}
				if got+1e-9 < d.Qty {
					fmt.Fprintf(os.Stderr, "warning: pantry shortfall accepted for %s: needed %.3f %s, only %.3f on hand\n", item.Name, d.Qty, unit, got)
				}
				if !costKnown {
					anyCostUnknown = true
				}
				depletedCost += itemCost
				for _, l := range lots {
					drawnLots = append(drawnLots, store.ConsumptionDepletion{
						ItemID: d.ItemID, PantryStockID: l.StockID, Quantity: l.Quantity, Unit: l.Unit,
					})
				}
				depleted = append(depleted, pantryDepletedItem{ItemID: d.ItemID, Name: item.Name, Quantity: got, Unit: unit, CostKnown: costKnown})
			}
			if len(depletions) > 0 && !cmd.Flags().Changed("cost") {
				entry.Cost = depletedCost
				cost = depletedCost
				if anyCostUnknown {
					fmt.Fprintf(os.Stderr, "warning: cost unknown for at least one depleted item (no purchase history yet), logged cost is a partial sum (set with --cost X to override)\n")
				}
			}

			id, err := store.InsertConsumptionEntry(tx, entry)
			if err != nil {
				return fmt.Errorf("logging consumption: %w", err)
			}
			// Same transaction as the decrement, so the ledger of what this
			// entry took can never disagree with the stock it took it from.
			for _, l := range drawnLots {
				l.ConsumptionLogID = id
				if _, err := store.InsertConsumptionDepletion(tx, l); err != nil {
					return fmt.Errorf("recording pantry depletion: %w", err)
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing: %w", err)
			}

			result := logResult{
				ConsumptionID: id, ConsumedDate: date, Person: person, Source: source,
				Servings: servings, Cost: cost, PantryDepleted: depleted,
			}
			return renderLogResult(result)
		},
	}

	cmd.Flags().StringVar(&person, "person", "", "patrick or thea (required)")
	cmd.MarkFlagRequired("person")
	cmd.Flags().StringVar(&mealSlot, "meal-slot", "", "breakfast, lunch, dinner, or snack")
	cmd.Flags().StringVar(&date, "date", "", "date eaten, YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&fromLeftovers, "from-leftovers", "", "\"latest\" or a cook event id to draw servings from")
	cmd.Flags().Float64Var(&servings, "servings", 1, "servings eaten")
	cmd.Flags().Float64Var(&cost, "cost", 0, "cost of this ad-hoc entry (ignored with --from-leftovers, which is always $0)")
	cmd.Flags().StringVar(&source, "source", "purchased_ready", "purchased_ready, eaten_out, or from_pantry (ignored with --from-leftovers; from_pantry is set automatically by --deplete-pantry)")
	cmd.Flags().StringVar(&depletePantry, "deplete-pantry", "", "decrement real pantry stock for this entry, e.g. \"12=1,7=200:g\" (pantry item id=qty[:unit], unit defaults to the item's default_unit); sets --source from_pantry and, unless --cost is given, computes cost from actual purchase history")
	cmd.Flags().StringVar(&acceptZeroStock, "accept-zero-stock", "", "pantry item ids (comma-separated) to deplete from even if on-hand stock is less than needed (or zero) -- a per-item decision, only relevant with --deplete-pantry")
	cmd.AddCommand(newLogVoidCmd())
	cmd.AddCommand(newLogSetMacrosCmd())
	cmd.Flags().Float64Var(&calories, "calories", 0, "calories for this logged amount (not per serving)")
	cmd.Flags().Float64Var(&protein, "protein", 0, "protein grams for this logged amount")
	cmd.Flags().Float64Var(&carbs, "carbs", 0, "carb grams for this logged amount")
	cmd.Flags().Float64Var(&fat, "fat", 0, "fat grams for this logged amount")
	cmd.Flags().Float64Var(&fiber, "fiber", 0, "fiber grams for this logged amount")
	cmd.Flags().StringVar(&macroSource, "macro-source", "estimated", "provenance of any macro values given: local_meta, usda, or estimated")
	cmd.Flags().Float64Var(&grams, "grams", 0, "gram weight of this entry - triggers automatic nutrition lookup (cache -> USDA -> estimate) if no explicit macro flags are given")
	cmd.Flags().IntVar(&reuseFDCID, "reuse-fdc-id", 0, "confirm this is the same food as an earlier near-match candidate, and reuse its cached nutrition")
	cmd.Flags().BoolVar(&forceFreshLookup, "force-fresh-lookup", false, "skip near-match candidates entirely and do a fresh USDA/estimate lookup")
	return cmd
}

// resolveMacrosFor runs the macro-resolution chain, building a USDA client
// only if a real API key is configured (config.USDAAPIKey already excludes
// the unedited placeholder value). The returned MacroResolution has EITHER
// Resolved (safe to use) OR Candidates (needs an explicit decision) set -
// never both, never neither.
func resolveMacrosFor(cmd *cobra.Command, db *sql.DB, query string) (domain.MacroResolution, error) {
	client, err := usdaClientFromConfig()
	if err != nil {
		return domain.MacroResolution{}, err
	}
	return domain.ResolveFoodMacros(cmd.Context(), db, client, query)
}

func forceFreshMacrosFor(cmd *cobra.Command, db *sql.DB, query string) (domain.ResolvedMacros, error) {
	client, err := usdaClientFromConfig()
	if err != nil {
		return domain.ResolvedMacros{}, err
	}
	return domain.ForceFreshFoodMacros(cmd.Context(), db, client, query)
}

func usdaClientFromConfig() (*usda.Client, error) {
	key, err := config.USDAAPIKey()
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, nil
	}
	return usda.NewClient(key), nil
}

// renderNutritionCandidates prints near-match candidates (never auto-applied)
// and returns an error, since nothing was logged - the caller must re-run
// with --reuse-fdc-id or --force-fresh-lookup.
func renderNutritionCandidates(query string, candidates []domain.MacroCandidate) error {
	err := output.Render(os.Stdout, jsonOutput, map[string]any{
		"needs_decision": true,
		"query":          query,
		"candidates":     candidates,
	}, func() output.Table {
		rows := make([][]string, 0, len(candidates))
		for _, c := range candidates {
			rows = append(rows, []string{fmt.Sprintf("%d", c.FDCID), c.QueryText, c.Description, fmt.Sprintf("%.0f%%", c.Similarity*100)})
		}
		return output.Table{Headers: []string{"FDC ID", "PRIOR QUERY", "DESCRIPTION", "SIMILARITY"}, Rows: rows}
	})
	if err != nil {
		return err
	}
	return fmt.Errorf("nutrition for %q needs a decision: re-run with --reuse-fdc-id <id> if one of the above is really the same food, or --force-fresh-lookup if none are", query)
}

func logFromLeftovers(db *sql.DB, label, person string, slotPtr *string, date string, servings float64, fromLeftovers string) error {
	var ce store.CookEvent
	var err error
	if fromLeftovers == "latest" {
		ce, err = store.LatestOpenCookEvent(db, "")
		if err != nil {
			return fmt.Errorf("no open leftovers found: %w", err)
		}
	} else {
		id, parseErr := strconv.ParseInt(fromLeftovers, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("--from-leftovers must be \"latest\" or a numeric cook event id, got %q", fromLeftovers)
		}
		ce, err = store.GetCookEvent(db, id)
		if err != nil {
			return fmt.Errorf("cook event %d not found: %w", id, err)
		}
	}

	meta, err := store.GetRecipeMeta(db, ce.RecipeID)
	if err != nil {
		return fmt.Errorf("loading recipe metadata: %w", err)
	}

	entry := store.ConsumptionEntry{
		ConsumedDate: date, Person: person, MealSlot: slotPtr, Source: "leftover",
		RecipeID: &ce.RecipeID, CookEventID: &ce.ID, FreeTextFood: &label, Servings: servings, Cost: 0,
	}
	// Cost is $0 for a leftover (the batch was charged in full at cook
	// time) but the macros are not: eating a serving delivers them again.
	macroRes, err := resolveCookEventMacros(db, ce, meta)
	if err != nil {
		return err
	}
	warnIfNoMacros(ce.RecipeID, macroRes)
	applyRecipeMacros(&entry, macroRes, servings)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	if err := store.DecrementCookEventServings(tx, ce.ID, servings); err != nil {
		return err
	}
	id, err := store.InsertConsumptionEntry(tx, entry)
	if err != nil {
		return fmt.Errorf("logging consumption: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	remaining := ce.ServingsRemaining - servings
	result := logResult{
		ConsumptionID: id, ConsumedDate: date, Person: person, Source: "leftover",
		RecipeID: &ce.RecipeID, CookEventID: &ce.ID, Servings: servings, Cost: 0, ServingsRemaining: &remaining,
	}
	return renderLogResult(result)
}

func renderLogResult(result logResult) error {
	return output.Render(os.Stdout, jsonOutput, result, func() output.Table {
		rows := [][]string{
			{"consumption id", fmt.Sprintf("%d", result.ConsumptionID)},
			{"date", result.ConsumedDate},
			{"person", result.Person},
			{"source", result.Source},
			{"servings", fmt.Sprintf("%.2f", result.Servings)},
			{"cost", fmt.Sprintf("$%.2f", result.Cost)},
		}
		if result.ServingsRemaining != nil {
			rows = append(rows, []string{"leftovers remaining", fmt.Sprintf("%.2f", *result.ServingsRemaining)})
		}
		for _, d := range result.PantryDepleted {
			cost := "known"
			if !d.CostKnown {
				cost = "unknown"
			}
			rows = append(rows, []string{"pantry depleted", fmt.Sprintf("%s: %.3f %s (cost %s)", d.Name, d.Quantity, d.Unit, cost)})
		}
		return output.Table{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
	})
}

// newLogVoidCmd removes an erroneous consumption entry, putting back any
// pantry stock it drew from (see store.VoidLogEntry). Entries logged before
// consumption_depletions existed have nothing recorded to give back; the
// command says so plainly rather than implying the pantry is now correct.
func newLogVoidCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "void <consumption-log-id>",
		Short: "Delete an erroneous consumption log entry, putting back any pantry stock it drew from",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid consumption log id %q", args[0])
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			v, err := store.VoidLogEntry(db, id)
			if err != nil {
				return err
			}
			if err := output.Render(os.Stdout, jsonOutput, v, func() output.Table {
				rows := [][]string{
					{"deleted entry", fmt.Sprintf("%d", v.ID)},
					{"date", v.ConsumedDate},
					{"person", v.Person},
					{"meal slot", v.MealSlot},
					{"food", v.Food},
					{"cost removed", fmt.Sprintf("$%.2f", v.Cost)},
					{"source", v.Source},
				}
				if v.CookEventID != nil && v.ServingsReturned != 0 {
					rows = append(rows, []string{"servings returned",
						fmt.Sprintf("%.2f to cook event %d", v.ServingsReturned, *v.CookEventID)})
				}
				for _, r := range v.RestoredLots {
					rows = append(rows, []string{"stock restored", fmt.Sprintf("%s: %.3f %s (lot %d)", r.ItemName, r.Quantity, r.Unit, r.StockID)})
				}
				return output.Table{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
			}); err != nil {
				return err
			}
			if !jsonOutput && v.Source == "from_pantry" && len(v.RestoredLots) == 0 {
				fmt.Println("\nNOTE: no pantry stock was restored. This entry predates the depletion ledger, so what it")
				fmt.Println("drew from was never recorded - add the quantities back by hand with `meal pantry adjust`.")
			}
			return nil
		},
	}
}

// newLogSetMacrosCmd fills in or corrects the macros on an entry that's
// already logged, without touching anything else about it.
//
// The alternative - void and re-log - can't be used for a macro-only fix on
// an entry that depleted the pantry: voiding doesn't give the stock back
// (nothing records which lots an entry drew from), so re-logging decrements
// a second time and the correction has to be chased with compensating
// `pantry adjust` calls. That's a lot of moving parts for changing five
// numbers.
func newLogSetMacrosCmd() *cobra.Command {
	var calories, protein, carbs, fat, fiber float64
	var macroSource string
	cmd := &cobra.Command{
		Use:   "set-macros <consumption-log-id>",
		Short: "Set or correct the macros on an already-logged entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid consumption log id %q", args[0])
			}
			if !validMacroSources[macroSource] {
				return fmt.Errorf("invalid --macro-source %q: must be local_meta, usda, or estimated", macroSource)
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			tx, err := db.Begin()
			if err != nil {
				return fmt.Errorf("starting transaction: %w", err)
			}
			defer tx.Rollback()

			if err := store.UpdateConsumptionMacros(tx, id, calories, protein, carbs, fat, fiber); err != nil {
				return err
			}
			if err := store.SetConsumptionMacroSource(tx, id, macroSource); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("committing: %w", err)
			}

			return output.Render(os.Stdout, jsonOutput, map[string]any{
				"id": id, "calories": calories, "protein": protein, "carbs": carbs,
				"fat": fat, "fiber": fiber, "macro_source": macroSource,
			}, func() output.Table {
				return output.Table{
					Headers: []string{"FIELD", "VALUE"},
					Rows: [][]string{
						{"consumption id", fmt.Sprintf("%d", id)},
						{"calories", fmt.Sprintf("%.0f", calories)},
						{"protein", fmt.Sprintf("%.1f g", protein)},
						{"carbs", fmt.Sprintf("%.1f g", carbs)},
						{"fat", fmt.Sprintf("%.1f g", fat)},
						{"fiber", fmt.Sprintf("%.1f g", fiber)},
						{"macro source", macroSource},
					},
				}
			})
		},
	}
	cmd.Flags().Float64Var(&calories, "calories", 0, "calories for this logged amount (not per serving)")
	cmd.Flags().Float64Var(&protein, "protein", 0, "protein grams for this logged amount")
	cmd.Flags().Float64Var(&carbs, "carbs", 0, "carb grams for this logged amount")
	cmd.Flags().Float64Var(&fat, "fat", 0, "fat grams for this logged amount")
	cmd.Flags().Float64Var(&fiber, "fiber", 0, "fiber grams for this logged amount")
	cmd.Flags().StringVar(&macroSource, "macro-source", "usda", "provenance of these values: local_meta, usda, or estimated")
	return cmd
}
