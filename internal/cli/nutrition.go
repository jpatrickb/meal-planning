package cli

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
	"github.com/jpatrickb/meal-planning/internal/usda"
)

func newNutritionCmd() *cobra.Command {
	n := &cobra.Command{
		Use:   "nutrition",
		Short: "Inspect and repair the cached USDA nutrition data",
	}
	n.AddCommand(newNutritionRepairCmd())
	return n
}

// macroTolerance is how far a stored figure may drift from a recomputed one
// before it counts as a real difference. Everything here is a float that has
// been through a scale-and-store round trip, so exact equality is the wrong
// test; 0.01 is far below the precision any of these numbers actually carry.
const macroTolerance = 0.01

// cacheFix is one cached food whose macros read differently under the
// current parser than they did when the row was written.
type cacheFix struct {
	FDCID       int
	Description string
	Old         macroSet
	New         macroSet
}

// logFix is one consumption_log row whose stored macros were derived from a
// cache row that cacheFix is about to correct.
type logFix struct {
	ID          int64
	Date        string
	Person      string
	Food        string
	Grams       float64
	Old         macroSet
	New         macroSet
	SkipReason  string
	Description string
}

type macroSet struct {
	Calories, Protein, Carbs, Fat, Fiber float64
}

func (m macroSet) scaled(grams float64) macroSet {
	s := grams / 100
	return macroSet{m.Calories * s, m.Protein * s, m.Carbs * s, m.Fat * s, m.Fiber * s}
}

func (m macroSet) differsFrom(o macroSet) bool {
	return math.Abs(m.Calories-o.Calories) > macroTolerance ||
		math.Abs(m.Protein-o.Protein) > macroTolerance ||
		math.Abs(m.Carbs-o.Carbs) > macroTolerance ||
		math.Abs(m.Fat-o.Fat) > macroTolerance ||
		math.Abs(m.Fiber-o.Fiber) > macroTolerance
}

// newNutritionRepairCmd re-reads every cached USDA food from the raw JSON
// already stored alongside it, and fixes both the cache row and any
// consumption_log entry whose macros were computed from it.
//
// This exists because the original parser matched nutrients by name
// substring: "energy" matched USDA's kJ row as readily as its kcal row (the
// two share a name and differ only by unit), and "fat"+"total" matched every
// "Fatty acids, total ..." row. Whichever came last in the array won, so a
// subset of cached foods ended up holding kilojoules in calories_per_100g
// and a trans-fat figure in fat_per_100g.
//
// Re-deriving from the stored raw JSON rather than re-querying USDA is
// deliberate: a fresh search could return a *different* food than the one
// originally confirmed for that query, silently changing what a logged meal
// claims to have been.
func newNutritionRepairCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Re-read cached USDA foods with the current parser and correct any macros that were mis-read",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			cached, err := store.ListNutritionCache(db)
			if err != nil {
				return fmt.Errorf("listing nutrition cache: %w", err)
			}

			cacheFixes := map[int]cacheFix{}
			var unparsable []int
			var macroless []cacheFix
			for _, e := range cached {
				if e.RawJSON == "" {
					unparsable = append(unparsable, e.FDCID)
					continue
				}
				food, err := usda.FoodFromRawJSON(e.RawJSON)
				if err != nil {
					unparsable = append(unparsable, e.FDCID)
					continue
				}
				reparsed := macroSet{food.CaloriesPer100g, food.ProteinPer100g, food.CarbsPer100g, food.FatPer100g, food.FiberPer100g}
				old := macroSet{e.CaloriesPer100g, e.ProteinPer100g, e.CarbsPer100g, e.FatPer100g, e.FiberPer100g}
				if !food.HasProximates {
					// Zeroes here mean "USDA reported no macros for this
					// food", not "this food has none" - worth naming, since
					// nothing about the stored row says which it is.
					macroless = append(macroless, cacheFix{FDCID: e.FDCID, Description: e.Description, Old: old, New: reparsed})
				}
				if !reparsed.differsFrom(old) {
					continue
				}
				cacheFixes[e.FDCID] = cacheFix{FDCID: e.FDCID, Description: e.Description, Old: old, New: reparsed}
			}

			logFixes, err := planLogFixes(db, cacheFixes)
			if err != nil {
				return err
			}

			if !dryRun && len(cacheFixes) > 0 {
				if err := applyRepairs(db, cacheFixes, logFixes); err != nil {
					return err
				}
			}
			linkedItems, err := store.PantryItemNamesByFDCID(db)
			if err != nil {
				return fmt.Errorf("listing linked pantry items: %w", err)
			}
			return renderRepair(cacheFixes, logFixes, unparsable, macroless, linkedItems, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	return cmd
}

// planLogFixes works out which logged entries were computed from a cache row
// that's being corrected, and what their macros should now say.
//
// consumption_log stores absolute macros for the portion eaten, not the food
// id or the gram weight behind them, so both have to be recovered: the food
// via the confirmed query alias for the entry's own text, and the portion by
// dividing the stored calories by the (pre-fix) per-100g figure. That
// implied portion is then checked against all four other macros, and any
// entry where they don't agree is skipped rather than rescaled - the numbers
// disagreeing means the entry didn't come from this cache row the way we
// assume, and quietly rewriting it on a bad assumption is worse than leaving
// a known-wrong number for a human to look at.
func planLogFixes(db *sql.DB, cacheFixes map[int]cacheFix) ([]logFix, error) {
	entries, err := store.ListConsumptionEntries(db, store.ConsumptionListOpts{})
	if err != nil {
		return nil, fmt.Errorf("listing consumption log: %w", err)
	}

	var out []logFix
	for _, e := range entries {
		if e.MacroSource == nil || *e.MacroSource != "usda" || e.Calories == nil || e.FreeTextFood == nil {
			continue
		}
		fdcID, ok, err := store.GetNutritionAliasExact(db, domain.NormalizeFoodQuery(*e.FreeTextFood))
		if err != nil {
			return nil, fmt.Errorf("looking up nutrition alias: %w", err)
		}
		if !ok {
			continue
		}
		fix, changing := cacheFixes[fdcID]
		if !changing {
			continue
		}

		stored := macroSet{
			Calories: *e.Calories,
			Protein:  derefOrZero(e.Protein),
			Carbs:    derefOrZero(e.Carbs),
			Fat:      derefOrZero(e.Fat),
			Fiber:    derefOrZero(e.Fiber),
		}
		lf := logFix{
			ID: e.ID, Date: e.ConsumedDate, Person: e.Person, Food: *e.FreeTextFood,
			Old: stored, Description: fix.Description,
		}
		if fix.Old.Calories <= 0 {
			lf.SkipReason = "cached food had no calorie figure to scale from"
			out = append(out, lf)
			continue
		}
		grams := stored.Calories / fix.Old.Calories * 100
		lf.Grams = grams
		if expected := fix.Old.scaled(grams); expected.differsFrom(stored) {
			lf.SkipReason = "stored macros don't match this food at any one portion size"
			out = append(out, lf)
			continue
		}
		lf.New = fix.New.scaled(grams)
		out = append(out, lf)
	}
	return out, nil
}

func applyRepairs(db *sql.DB, cacheFixes map[int]cacheFix, logFixes []logFix) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	for _, f := range cacheFixes {
		if err := store.UpdateNutritionMacros(tx, f.FDCID, f.New.Calories, f.New.Protein, f.New.Carbs, f.New.Fat, f.New.Fiber); err != nil {
			return fmt.Errorf("updating cached food %d: %w", f.FDCID, err)
		}
	}
	for _, f := range logFixes {
		if f.SkipReason != "" {
			continue
		}
		if err := store.UpdateConsumptionMacros(tx, f.ID, f.New.Calories, f.New.Protein, f.New.Carbs, f.New.Fat, f.New.Fiber); err != nil {
			return fmt.Errorf("updating log entry %d: %w", f.ID, err)
		}
	}
	return tx.Commit()
}

func renderRepair(cacheFixes map[int]cacheFix, logFixes []logFix, unparsable []int, macroless []cacheFix, linkedItems map[int][]string, dryRun bool) error {
	type repairReport struct {
		DryRun            bool
		CachedFoodsFix    []cacheFix
		LogEntriesFix     []logFix
		UnparsableFDCID   []int
		FoodsWithNoMacros []cacheFix
	}
	foods := make([]cacheFix, 0, len(cacheFixes))
	for _, f := range cacheFixes {
		foods = append(foods, f)
	}
	report := repairReport{DryRun: dryRun, CachedFoodsFix: foods, LogEntriesFix: logFixes,
		UnparsableFDCID: unparsable, FoodsWithNoMacros: macroless}

	if jsonOutput {
		return output.Render(os.Stdout, true, report, func() output.Table { return output.Table{} })
	}

	if len(cacheFixes) == 0 {
		fmt.Println("Every cached food re-reads the same way it was stored; nothing to repair.")
		reportMacroless(macroless, linkedItems)
		return nil
	}

	verb := "Corrected"
	if dryRun {
		verb = "Would correct"
	}
	rows := make([][]string, 0, len(foods))
	for _, f := range foods {
		rows = append(rows, []string{
			fmt.Sprintf("%d", f.FDCID), truncate(f.Description, 44),
			fmt.Sprintf("%.0f -> %.0f", f.Old.Calories, f.New.Calories),
			fmt.Sprintf("%.1f -> %.1f", f.Old.Fat, f.New.Fat),
			fmt.Sprintf("%.1f -> %.1f", f.Old.Protein, f.New.Protein),
			fmt.Sprintf("%.1f -> %.1f", f.Old.Carbs, f.New.Carbs),
		})
	}
	fmt.Printf("%s %d cached food(s), per 100g:\n\n", verb, len(foods))
	if err := output.Render(os.Stdout, false, nil, func() output.Table {
		return output.Table{Headers: []string{"FDC ID", "FOOD", "CALORIES", "FAT", "PROTEIN", "CARBS"}, Rows: rows}
	}); err != nil {
		return err
	}

	var rescaled, skipped []logFix
	for _, f := range logFixes {
		if f.SkipReason == "" {
			rescaled = append(rescaled, f)
		} else {
			skipped = append(skipped, f)
		}
	}

	if len(rescaled) > 0 {
		logRows := make([][]string, 0, len(rescaled))
		for _, f := range rescaled {
			logRows = append(logRows, []string{
				fmt.Sprintf("%d", f.ID), f.Date, f.Person, truncate(f.Food, 36),
				fmt.Sprintf("%.0fg", f.Grams),
				fmt.Sprintf("%.0f -> %.0f", f.Old.Calories, f.New.Calories),
				fmt.Sprintf("%.1f -> %.1f", f.Old.Protein, f.New.Protein),
			})
		}
		fmt.Printf("\n%s %d logged entr(ies) computed from those foods:\n\n", verb, len(rescaled))
		if err := output.Render(os.Stdout, false, nil, func() output.Table {
			return output.Table{Headers: []string{"LOG ID", "DATE", "PERSON", "FOOD", "PORTION", "CALORIES", "PROTEIN"}, Rows: logRows}
		}); err != nil {
			return err
		}
	}

	if len(skipped) > 0 {
		fmt.Printf("\n%d logged entr(ies) were left alone and need a look:\n", len(skipped))
		for _, f := range skipped {
			fmt.Printf("  log %d (%s, %s, %q): %s\n", f.ID, f.Date, f.Person, f.Food, f.SkipReason)
		}
		fmt.Println("  Void and re-log these if their macros matter.")
	}

	if len(unparsable) > 0 {
		fmt.Printf("\n%d cached food(s) had no re-readable raw JSON and were skipped: %v\n", len(unparsable), unparsable)
	}

	reportMacroless(macroless, linkedItems)

	if dryRun {
		fmt.Println("\nDry run: nothing was written.")
	}
	return nil
}

// reportMacroless names the cached foods USDA returned without any macro
// nutrients. Their stored zeroes are indistinguishable from a food that
// genuinely has none, so the fix is a human deciding: re-link to a better
// entry, or clear the link and leave it honestly unknown.
func reportMacroless(macroless []cacheFix, linkedItems map[int][]string) {
	if len(macroless) == 0 {
		return
	}
	fmt.Printf("\n%d cached food(s) carry no macro data at all (stored as zeroes, which may not mean zero):\n", len(macroless))
	for _, f := range macroless {
		items := linkedItems[f.FDCID]
		if len(items) == 0 {
			fmt.Printf("  %d %s - not linked to any pantry item\n", f.FDCID, f.Description)
			continue
		}
		fmt.Printf("  %d %s - pantry: %s\n", f.FDCID, f.Description, strings.Join(items, ", "))
	}
	fmt.Println("  Re-link these with `meal pantry link-nutrition --item-id N --force --query \"...\"`, or `--clear` to leave them honestly unknown.")
}

func derefOrZero(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
