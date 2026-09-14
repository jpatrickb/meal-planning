package domain

import (
	"database/sql"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// RecipeCostResult is the outcome of summing a recipe's ingredient-based
// cost. AllKnown is false whenever at least one mapped ingredient has no
// known unit cost yet (no purchase has ever been recorded in that exact
// unit) -- in that case Cost is a partial sum, a lower bound, not the real
// total, matching the never-fabricate principle used throughout this
// project. not_tracked ingredients never contribute -- that's the point of
// marking them that way.
type RecipeCostResult struct {
	Cost         float64
	AllKnown     bool
	UnknownItems []string
}

// ComputeRecipeCost sums known per-ingredient costs from a recipe's ingredient
// mapping. It doesn't check mapping completeness itself (see
// ComputeMappingStatus / ResolveRecipeCost for that) -- the result is only
// meaningful once the mapping is complete.
func ComputeRecipeCost(db *sql.DB, recipeID string) (RecipeCostResult, error) {
	rows, err := store.ListRecipeIngredients(db, recipeID)
	if err != nil {
		return RecipeCostResult{}, err
	}

	result := RecipeCostResult{AllKnown: true}
	for _, ri := range rows {
		if ri.Status != "mapped" {
			continue
		}
		if *ri.Quantity == 0 {
			// A zero-quantity mapping (an optional ingredient not used by
			// default, docs §11) contributes nothing regardless of price --
			// no need to know its cost for the total to be accurate.
			continue
		}
		unitCost, ok, err := store.GetItemUnitCost(db, *ri.ItemID, *ri.Unit, UnitConverterFor(db, *ri.ItemID))
		if err != nil {
			return RecipeCostResult{}, err
		}
		if !ok {
			result.AllKnown = false
			if item, ierr := store.GetPantryItem(db, *ri.ItemID); ierr == nil {
				result.UnknownItems = append(result.UnknownItems, item.Name)
			}
			continue
		}
		result.Cost += unitCost * (*ri.Quantity)
	}
	return result, nil
}

// CostResolution is the outcome of deciding what cost to show/charge for a
// recipe: a manual recipe_meta value
// always wins when present (whether the recipe isn't mapped yet, or someone
// deliberately kept an override after completion); "computed" only applies
// once the ingredient mapping is complete AND every ingredient's cost is
// actually known; otherwise the cost is honestly unknown, not zero.
type CostResolution struct {
	Cost         float64
	Source       string // "manual", "computed", or "unknown"
	UnknownItems []string
}

// ResolveRecipeCost is the single resolution point both `meal recipe show`
// and `meal cook` use, so display and charging never disagree.
func ResolveRecipeCost(db *sql.DB, recipeID string, meta store.RecipeMeta) (CostResolution, error) {
	if meta.CostTotal != nil {
		return CostResolution{Cost: *meta.CostTotal, Source: "manual"}, nil
	}

	recipe, err := store.GetRecipe(db, recipeID)
	if err != nil {
		return CostResolution{}, err
	}
	existing, err := store.ListRecipeIngredients(db, recipeID)
	if err != nil {
		return CostResolution{}, err
	}
	status := ComputeMappingStatus(FlattenIngredientLines(recipe.IngredientGroups), existing)
	if status.State != "complete" {
		return CostResolution{Source: "unknown"}, nil
	}

	result, err := ComputeRecipeCost(db, recipeID)
	if err != nil {
		return CostResolution{}, err
	}
	if !result.AllKnown {
		return CostResolution{Source: "unknown", UnknownItems: result.UnknownItems}, nil
	}
	return CostResolution{Cost: result.Cost, Source: "computed"}, nil
}
