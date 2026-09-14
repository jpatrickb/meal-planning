package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/output"
	"github.com/jpatrickb/meal-planning/internal/store"
)

func newRecipeCmd() *cobra.Command {
	recipe := &cobra.Command{
		Use:   "recipe",
		Short: "Browse and annotate synced recipes",
	}
	recipe.AddCommand(newRecipeListCmd())
	recipe.AddCommand(newRecipeShowCmd())
	recipe.AddCommand(newRecipeSetMetaCmd())
	recipe.AddCommand(newRecipeBackfillMacrosCmd())
	recipe.AddCommand(newRecipeMapIngredientsCmd())
	return recipe
}

func newRecipeListCmd() *cobra.Command {
	var category, tag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List synced recipes",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			summaries, err := store.ListRecipeSummaries(db, store.ListRecipesOpts{Category: category, Tag: tag})
			if err != nil {
				return fmt.Errorf("listing recipes: %w", err)
			}
			if len(summaries) == 0 {
				fmt.Fprintln(os.Stderr, "no recipes cached yet - run `meal sync recipes` first")
			}

			return output.Render(os.Stdout, jsonOutput, summaries, func() output.Table {
				rows := make([][]string, 0, len(summaries))
				for _, s := range summaries {
					rating := "-"
					if s.Rating != nil {
						rating = fmt.Sprintf("%d", *s.Rating)
					}
					lastCooked := "never"
					if s.DaysSinceCooked != nil {
						lastCooked = fmt.Sprintf("%dd ago", *s.DaysSinceCooked)
					}
					rows = append(rows, []string{
						s.ID, s.Emoji + " " + s.Title, s.Category, s.ServesRaw,
						rating, fmt.Sprintf("%d", s.TimesCooked), lastCooked,
					})
				}
				return output.Table{
					Headers: []string{"ID", "TITLE", "CATEGORY", "SERVES", "RATING", "COOKED", "LAST COOKED"},
					Rows:    rows,
				}
			})
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "filter by category")
	cmd.Flags().StringVar(&tag, "tag", "", "filter by tag")
	return cmd
}

type recipeDetail struct {
	store.Recipe
	Meta   store.RecipeMeta             `json:"meta"`
	Cost   domain.CostResolution        `json:"cost"`
	Macros domain.RecipeMacroResolution `json:"macros"`
}

func newRecipeShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <slug>",
		Short: "Show full recipe detail including local metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			r, err := store.GetRecipe(db, args[0])
			if err != nil {
				return fmt.Errorf("recipe %q not found (try `meal recipe list` or `meal sync recipes`): %w", args[0], err)
			}
			meta, err := store.GetRecipeMeta(db, args[0])
			if err != nil {
				return fmt.Errorf("loading recipe metadata: %w", err)
			}
			costRes, err := domain.ResolveRecipeCost(db, args[0], meta)
			if err != nil {
				return fmt.Errorf("resolving cost: %w", err)
			}
			// Servings only matter for the per-serving division; an
			// unresolvable serving count is reported as unknown macros
			// rather than failing the whole show.
			servings, _ := domain.ResolveServings(nil, meta.ServesOverride, r.ServesRaw, args[0])
			macroRes, err := domain.ResolveRecipeMacros(db, args[0], meta, float64(servings))
			if err != nil {
				return fmt.Errorf("resolving macros: %w", err)
			}
			detail := recipeDetail{Recipe: r, Meta: meta, Cost: costRes, Macros: macroRes}

			if jsonOutput {
				return output.Render(os.Stdout, true, detail, nil)
			}
			printRecipeDetail(detail)
			return nil
		},
	}
}

// macrosDisagreeThreshold is how far a hand-entered per-serving figure may
// sit from the one the ingredients compute to before it's worth flagging.
// Generous on purpose: the computed figure has its own error bars (estimated
// gram anchors, generic USDA entries), so only a difference too large to
// come from that is worth a reader's attention.
const macrosDisagreeThreshold = 0.4

func macrosDisagree(manual, computed store.MacrosPerServing) bool {
	if computed.Calories <= 0 {
		return false
	}
	diff := manual.Calories/computed.Calories - 1
	return diff > macrosDisagreeThreshold || diff < -macrosDisagreeThreshold
}

func printRecipeDetail(d recipeDetail) {
	fmt.Printf("%s %s  (%s)\n", d.Emoji, d.Title, d.ID)
	fmt.Printf("Category: %s | Tags: %s | Serves: %s\n", d.Category, strings.Join(d.Tags, ", "), emptyDash(d.ServesRaw))
	if d.Meta.Rating != nil {
		fmt.Printf("Rating: %d/5\n", *d.Meta.Rating)
	}
	switch d.Cost.Source {
	case "manual":
		fmt.Printf("Cost (total recipe, manual): $%.2f\n", d.Cost.Cost)
	case "computed":
		fmt.Printf("Cost (total recipe, computed from ingredients): $%.2f\n", d.Cost.Cost)
	default:
		if len(d.Cost.UnknownItems) > 0 {
			fmt.Printf("Cost: unknown (no purchase history yet for: %s)\n", strings.Join(d.Cost.UnknownItems, ", "))
		}
	}
	if d.Meta.ServesOverride != nil {
		fmt.Printf("Serves override: %d\n", *d.Meta.ServesOverride)
	}
	switch d.Macros.Source {
	case "manual", "computed":
		m := d.Macros.PerServing
		how := "manual"
		if d.Macros.Source == "computed" {
			how = "computed from ingredients"
		}
		fmt.Printf("Macros/serving (%s): %.0f cal, %.1fg protein, %.1fg carbs, %.1fg fat, %.1fg fiber\n",
			how, m.Calories, m.Protein, m.Carbs, m.Fat, m.Fiber)
		if c := d.Macros.ComputedPerServing; c != nil && macrosDisagree(m, *c) {
			fmt.Printf("  NOTE: the ingredients compute to %.0f cal, %.1fg protein, %.1fg carbs, %.1fg fat per serving.\n",
				c.Calories, c.Protein, c.Carbs, c.Fat)
			fmt.Printf("  The manual value is being used. Clear it with `meal recipe set-meta %s --clear-macros` if the computed one is better.\n", d.ID)
		}
	default:
		if t := d.Macros.Total; t.Calories > 0 {
			fmt.Printf("Macros (whole batch, computed from ingredients): %.0f cal, %.1fg protein, %.1fg carbs, %.1fg fat, %.1fg fiber\n",
				t.Calories, t.Protein, t.Carbs, t.Fat, t.Fiber)
		}
		if d.Macros.Reason != "" {
			if len(d.Macros.UnknownItems) > 0 {
				fmt.Printf("Macros/serving: unknown - %s: %s\n", d.Macros.Reason, strings.Join(d.Macros.UnknownItems, "; "))
			} else {
				fmt.Printf("Macros/serving: unknown - %s\n", d.Macros.Reason)
			}
		}
	}
	fmt.Println("\nIngredients:")
	for _, g := range d.IngredientGroups {
		if g.Heading != nil && *g.Heading != "" {
			fmt.Printf("  %s:\n", *g.Heading)
		}
		for _, item := range g.Items {
			fmt.Printf("  - %s\n", item)
		}
	}
	fmt.Println("\nInstructions:")
	for i, step := range d.Instructions {
		fmt.Printf("  %d. %s\n", i+1, step)
	}
	if d.Meta.Notes != nil && *d.Meta.Notes != "" {
		fmt.Printf("\nNotes: %s\n", *d.Meta.Notes)
	}
}

func newRecipeSetMetaCmd() *cobra.Command {
	var serves int
	var cost, calories, protein, carbs, fat, fiber float64
	var clearMacros bool
	var rating int
	var notes string
	var clearCost bool
	var setServes, setCost, setRating, setNotes, setMacros bool

	cmd := &cobra.Command{
		Use:   "set-meta <slug>",
		Short: "Set local metadata (serves override, cost, rating, macros/serving, notes) for a recipe",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			if setCost && clearCost {
				return fmt.Errorf("--cost and --clear-cost are mutually exclusive")
			}
			_, db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()

			if _, err := store.GetRecipe(db, slug); err != nil {
				return fmt.Errorf("recipe %q not found (try `meal recipe list` or `meal sync recipes`): %w", slug, err)
			}

			meta, err := store.GetRecipeMeta(db, slug)
			if err != nil {
				return fmt.Errorf("loading existing metadata: %w", err)
			}
			meta.RecipeID = slug

			if setServes {
				meta.ServesOverride = &serves
			}
			if setCost {
				meta.CostTotal = &cost
			}
			if clearCost {
				meta.CostTotal = nil
			}
			if setRating {
				meta.Rating = &rating
			}
			if setNotes {
				meta.Notes = &notes
			}
			if setMacros {
				meta.MacrosPerServing = &store.MacrosPerServing{
					Calories: calories, Protein: protein, Carbs: carbs, Fat: fat, Fiber: fiber,
				}
			}
			if clearMacros {
				meta.MacrosPerServing = nil
			}

			if err := store.UpsertRecipeMeta(db, meta); err != nil {
				return fmt.Errorf("saving metadata: %w", err)
			}
			return renderResult(map[string]any{"recipe_id": slug, "updated": true}, "Updated metadata for %s\n", slug)
		},
	}
	cmd.Flags().IntVar(&serves, "serves", 0, "override the site's serves value with a concrete integer")
	cmd.Flags().Float64Var(&cost, "cost", 0, "total cost of the full recipe (charged once, at first cook)")
	cmd.Flags().BoolVar(&clearCost, "clear-cost", false, "clear the manual cost so a complete ingredient mapping's computed cost takes over (mutually exclusive with --cost)")
	cmd.Flags().IntVar(&rating, "rating", 0, "your rating, 1-5")
	cmd.Flags().StringVar(&notes, "notes", "", "free-text notes")
	cmd.Flags().Float64Var(&calories, "calories", 0, "calories per serving")
	cmd.Flags().Float64Var(&protein, "protein", 0, "protein grams per serving")
	cmd.Flags().Float64Var(&carbs, "carbs", 0, "carb grams per serving")
	cmd.Flags().Float64Var(&fat, "fat", 0, "fat grams per serving")
	cmd.Flags().Float64Var(&fiber, "fiber", 0, "fiber grams per serving")
	cmd.Flags().BoolVar(&clearMacros, "clear-macros", false, "clear the manual macros so a complete ingredient mapping's computed macros take over (mutually exclusive with the macro flags)")

	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		setServes = cmd.Flags().Changed("serves")
		setCost = cmd.Flags().Changed("cost")
		setRating = cmd.Flags().Changed("rating")
		setNotes = cmd.Flags().Changed("notes")
		setMacros = cmd.Flags().Changed("calories") || cmd.Flags().Changed("protein") ||
			cmd.Flags().Changed("carbs") || cmd.Flags().Changed("fat") || cmd.Flags().Changed("fiber")
		if setMacros && clearMacros {
			return fmt.Errorf("--clear-macros and the macro flags are mutually exclusive")
		}
		return nil
	}
	return cmd
}
