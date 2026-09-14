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

func newRecipeMapIngredientsCmd() *cobra.Command {
	var line int
	var notTracked, createNew, estimated bool
	var itemID int64
	var name, category, unit, lineUnit string
	var qty float64

	cmd := &cobra.Command{
		Use:   "map-ingredients <slug>",
		Short: "View or resolve a recipe's raw ingredient lines against pantry items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			recipe, err := store.GetRecipe(db, slug)
			if err != nil {
				return fmt.Errorf("recipe %q not found (try `meal recipe list` or `meal sync recipes`): %w", slug, err)
			}
			lines := domain.FlattenIngredientLines(recipe.IngredientGroups)
			if len(lines) == 0 {
				return fmt.Errorf("recipe %q has no ingredient lines to map", slug)
			}

			if !cmd.Flags().Changed("line") {
				return printMappingStatus(db, slug, lines)
			}
			if line < 1 || line > len(lines) {
				return fmt.Errorf("--line %d out of range: recipe has %d ingredient lines (run without --line to list them)", line, len(lines))
			}
			target := lines[line-1]

			_, hadRow, err := store.GetRecipeIngredientByLine(db, slug, target.Group, target.RawText)
			if err != nil {
				return fmt.Errorf("checking existing mapping: %w", err)
			}

			resolutionFlagsSet := notTracked || createNew || cmd.Flags().Changed("item-id")
			if !resolutionFlagsSet {
				return suggestIngredientMatch(db, target)
			}

			chosenCount := 0
			if notTracked {
				chosenCount++
			}
			if createNew {
				chosenCount++
			}
			if cmd.Flags().Changed("item-id") {
				chosenCount++
			}
			if chosenCount != 1 {
				return fmt.Errorf("choose exactly one of --not-tracked, --item-id, or --create-new")
			}

			ri := store.RecipeIngredient{
				RecipeID:          slug,
				IngredientGroup:   target.Group,
				RawIngredientText: target.RawText,
			}

			if notTracked {
				ri.Status = "not_tracked"
			} else {
				if !cmd.Flags().Changed("qty") {
					return fmt.Errorf("--qty is required when mapping to a pantry item (use --qty 0 for an optional ingredient not used by default)")
				}
				if lineUnit == "" {
					return fmt.Errorf("--line-unit is required when mapping to a pantry item")
				}

				var resolvedItemID int64
				if createNew {
					if name == "" || category == "" || unit == "" {
						return fmt.Errorf("--create-new requires --name, --category, and --unit")
					}
					if err := validateCategory(category); err != nil {
						return err
					}
					if existingItem, ok, err := store.GetPantryItemByName(db, name); err != nil {
						return fmt.Errorf("checking for existing item: %w", err)
					} else if ok {
						return fmt.Errorf("item %q already exists (id %d) - use --item-id %d instead of --create-new", name, existingItem.ID, existingItem.ID)
					}
					newID, err := store.CreatePantryItem(db, name, category, unit)
					if err != nil {
						return fmt.Errorf("creating pantry item: %w", err)
					}
					resolvedItemID = newID
					fmt.Printf("Created pantry item %d (%s)\n", newID, name)
				} else {
					if _, err := store.GetPantryItem(db, itemID); err != nil {
						return fmt.Errorf("item %d not found (try `meal pantry list`): %w", itemID, err)
					}
					resolvedItemID = itemID
				}

				source := "recipe_stated"
				if estimated {
					source = "estimated"
				}
				ri.Status = "mapped"
				ri.ItemID = &resolvedItemID
				ri.Quantity = &qty
				ri.Unit = &lineUnit
				ri.QuantitySource = &source
			}

			if _, err := store.UpsertRecipeIngredient(db, ri); err != nil {
				return fmt.Errorf("saving mapping: %w", err)
			}

			if ri.Status == "not_tracked" {
				fmt.Printf("Line %d (%q): marked not pantry-tracked\n", line, target.RawText)
			} else {
				fmt.Printf("Line %d (%q): mapped to item %d, %.3f %s (%s)\n", line, target.RawText, *ri.ItemID, *ri.Quantity, *ri.Unit, *ri.QuantitySource)
			}

			// Only announce a completeness transition if this line was
			// previously unresolved -- correcting an already-resolved line
			// can't newly complete a recipe that was already complete.
			if !hadRow {
				allRows, err := store.ListRecipeIngredients(db, slug)
				if err != nil {
					return fmt.Errorf("re-checking mapping status: %w", err)
				}
				if domain.ComputeMappingStatus(lines, allRows).State == "complete" {
					printCompletionNotice(db, slug)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&line, "line", 0, "which ingredient line to resolve (1-based, from the listing view); omit to view status")
	cmd.Flags().BoolVar(&notTracked, "not-tracked", false, `mark this line as not pantry-tracked (e.g. "salt to taste")`)
	cmd.Flags().Int64Var(&itemID, "item-id", 0, "map this line to an existing pantry item")
	cmd.Flags().BoolVar(&createNew, "create-new", false, "create a new pantry item for this line")
	cmd.Flags().StringVar(&name, "name", "", "new item name (with --create-new)")
	cmd.Flags().StringVar(&category, "category", "", "new item category (with --create-new)")
	cmd.Flags().StringVar(&unit, "unit", "", "new item's default unit (with --create-new)")
	cmd.Flags().Float64Var(&qty, "qty", 0, "quantity this line uses (0 is valid for an optional ingredient not used by default)")
	cmd.Flags().StringVar(&lineUnit, "line-unit", "", "unit for --qty, as recorded for this recipe line")
	cmd.Flags().BoolVar(&estimated, "estimated", false, `mark the quantity as an estimate rather than recipe-stated (e.g. an unquantified "for serving" ingredient)`)
	return cmd
}

func printMappingStatus(db *sql.DB, slug string, lines []domain.IngredientLine) error {
	existing, err := store.ListRecipeIngredients(db, slug)
	if err != nil {
		return fmt.Errorf("loading existing mapping: %w", err)
	}
	byKey := map[string]store.RecipeIngredient{}
	for _, ri := range existing {
		byKey[ingredientLineKey(ri.IngredientGroup, ri.RawIngredientText)] = ri
	}
	status := domain.ComputeMappingStatus(lines, existing)

	type lineResult struct {
		Index          int      `json:"index"`
		Group          *string  `json:"group,omitempty"`
		Text           string   `json:"text"`
		Status         string   `json:"status"`
		ItemID         *int64   `json:"item_id,omitempty"`
		Quantity       *float64 `json:"quantity,omitempty"`
		Unit           *string  `json:"unit,omitempty"`
		QuantitySource *string  `json:"quantity_source,omitempty"`
	}
	results := make([]lineResult, 0, len(lines))
	for _, l := range lines {
		lr := lineResult{Index: l.Index, Group: l.Group, Text: l.RawText, Status: "unresolved"}
		if ri, ok := byKey[ingredientLineKey(l.Group, l.RawText)]; ok {
			lr.Status, lr.ItemID, lr.Quantity, lr.Unit, lr.QuantitySource = ri.Status, ri.ItemID, ri.Quantity, ri.Unit, ri.QuantitySource
		}
		results = append(results, lr)
	}

	type mappingStatusResult struct {
		RecipeID string       `json:"recipe_id"`
		State    string       `json:"state"`
		Total    int          `json:"total_lines"`
		Resolved int          `json:"resolved_lines"`
		Lines    []lineResult `json:"lines"`
	}
	result := mappingStatusResult{RecipeID: slug, State: status.State, Total: status.TotalLines, Resolved: status.ResolvedLines, Lines: results}

	if jsonOutput {
		return output.Render(os.Stdout, true, result, nil)
	}

	fmt.Printf("%s: %s (%d/%d lines resolved)\n", slug, status.State, status.ResolvedLines, status.TotalLines)
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		group := "-"
		if r.Group != nil {
			group = *r.Group
		}
		detail := "-"
		switch r.Status {
		case "mapped":
			detail = fmt.Sprintf("item %d, %.3f %s (%s)", *r.ItemID, *r.Quantity, *r.Unit, *r.QuantitySource)
		case "not_tracked":
			detail = "not tracked"
		}
		rows = append(rows, []string{fmt.Sprintf("%d", r.Index), group, r.Text, r.Status, detail})
	}
	return output.RenderTable(os.Stdout, output.Table{Headers: []string{"LINE", "GROUP", "TEXT", "STATUS", "DETAIL"}, Rows: rows})
}

func suggestIngredientMatch(db *sql.DB, target domain.IngredientLine) error {
	candidates, err := store.ListMatchCandidates(db)
	if err != nil {
		return fmt.Errorf("loading pantry catalog: %w", err)
	}
	if len(candidates) == 0 {
		fmt.Printf("Line %q: pantry catalog is empty - use --create-new to add a pantry item, or --not-tracked\n", target.RawText)
		return nil
	}

	normalized := domain.NormalizeReceiptText(target.RawText)
	var bestItemID int64
	var bestScore float64
	found := false
	for _, c := range candidates {
		score := domain.JaccardSimilarity(normalized, domain.NormalizeReceiptText(c.Text))
		if score > bestScore {
			bestScore, bestItemID, found = score, c.ItemID, true
		}
	}
	if found && bestScore >= domain.FuzzyMatchThreshold {
		item, err := store.GetPantryItem(db, bestItemID)
		if err != nil {
			return fmt.Errorf("loading candidate item: %w", err)
		}
		fmt.Printf("Line %q: closest pantry match is %q (item %d, %.0f%% similarity) - NOT saved yet; confirm with --item-id %d --qty Q --line-unit U, or use --create-new / --not-tracked\n",
			target.RawText, item.Name, item.ID, bestScore*100, item.ID)
		return nil
	}
	fmt.Printf("Line %q: no confident pantry match - use --item-id, --create-new, or --not-tracked\n", target.RawText)
	return nil
}

func printCompletionNotice(db *sql.DB, slug string) {
	meta, err := store.GetRecipeMeta(db, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: mapping complete for %s, but couldn't check existing manual cost/macros: %v\n", slug, err)
		return
	}
	fmt.Printf("\n%s: ingredient mapping is now COMPLETE.\n", slug)
	if meta.CostTotal == nil && meta.MacrosPerServing == nil {
		return
	}
	manualCost := "none set"
	if meta.CostTotal != nil {
		manualCost = fmt.Sprintf("$%.2f", *meta.CostTotal)
	}
	fmt.Printf("It still has a manually-set cost (%s) - computed cost/macros aren't wired up to reads yet (that's Phase 2); "+
		"once they are, you'll be asked whether to keep this manual value or switch to computed.\n", manualCost)
}

func ingredientLineKey(group *string, rawText string) string {
	g := ""
	if group != nil {
		g = *group
	}
	return g + "\x00" + rawText
}
