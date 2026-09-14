package domain

import (
	"database/sql"
	"fmt"

	"github.com/jpatrickb/meal-planning/internal/store"
	"github.com/jpatrickb/meal-planning/internal/usda"
)

// ItemMacros is per-100g macro data for a pantry item, plus where it came
// from. A label recorded on the item itself beats the item's USDA link:
// for a branded product the package in hand is better evidence than the
// nearest generic entry USDA happens to carry.
type ItemMacros struct {
	CaloriesPer100g float64
	ProteinPer100g  float64
	CarbsPer100g    float64
	FatPer100g      float64
	FiberPer100g    float64
	Source          string // "label" or "usda"
	Description     string
}

// GetItemMacros resolves one pantry item's per-100g macros, reporting false
// when the item has neither a label nor a usable USDA link.
func GetItemMacros(db *sql.DB, itemID int64) (ItemMacros, bool, error) {
	if label, ok, err := store.GetItemNutrition(db, itemID); err != nil {
		return ItemMacros{}, false, err
	} else if ok {
		return ItemMacros{
			CaloriesPer100g: label.CaloriesPer100g, ProteinPer100g: label.ProteinPer100g,
			CarbsPer100g: label.CarbsPer100g, FatPer100g: label.FatPer100g,
			FiberPer100g: label.FiberPer100g, Source: "label", Description: "product label",
		}, true, nil
	}

	item, err := store.GetPantryItem(db, itemID)
	if err != nil {
		return ItemMacros{}, false, err
	}
	if item.FDCID == nil {
		return ItemMacros{}, false, nil
	}
	cached, ok, err := store.GetNutritionByFDCID(db, *item.FDCID)
	if err != nil || !ok {
		return ItemMacros{}, false, err
	}
	// A cached food USDA returned with no macro nutrients at all stores as
	// zeroes, which would silently drag a recipe's total down rather than
	// admit a gap. A food that genuinely has no macros (salt, baking soda)
	// stores as zeroes too - the raw response is what tells them apart.
	if cached.CaloriesPer100g == 0 && cached.ProteinPer100g == 0 &&
		cached.CarbsPer100g == 0 && cached.FatPer100g == 0 {
		food, err := usda.FoodFromRawJSON(cached.RawJSON)
		if err != nil || !food.HasProximates {
			return ItemMacros{}, false, nil
		}
	}
	return ItemMacros{
		CaloriesPer100g: cached.CaloriesPer100g, ProteinPer100g: cached.ProteinPer100g,
		CarbsPer100g: cached.CarbsPer100g, FatPer100g: cached.FatPer100g,
		FiberPer100g: cached.FiberPer100g, Source: "usda", Description: cached.Description,
	}, true, nil
}

// RecipeMacroResult is the outcome of summing a recipe's ingredient-based
// macros. AllKnown is false whenever a mapped ingredient couldn't
// contribute - no nutrition data for the item, or no way to express its
// quantity in grams - in which case the totals are a partial sum, a lower
// bound, not the real figure. Mirrors RecipeCostResult exactly, including
// the rule that not_tracked and zero-quantity ingredients never contribute.
type RecipeMacroResult struct {
	Total        store.MacrosPerServing
	AllKnown     bool
	UnknownItems []string
}

// ComputeRecipeMacros sums a recipe's mapped ingredients into a whole-batch
// macro total. Each ingredient's quantity is converted to grams (see
// ConvertQuantity) and scaled against the item's per-100g macros.
func ComputeRecipeMacros(db *sql.DB, recipeID string) (RecipeMacroResult, error) {
	rows, err := store.ListRecipeIngredients(db, recipeID)
	if err != nil {
		return RecipeMacroResult{}, err
	}

	result := RecipeMacroResult{AllKnown: true}
	for _, ri := range rows {
		if ri.Status != "mapped" || *ri.Quantity == 0 {
			continue
		}
		unknown := func(reason string) {
			result.AllKnown = false
			if item, ierr := store.GetPantryItem(db, *ri.ItemID); ierr == nil {
				result.UnknownItems = append(result.UnknownItems, fmt.Sprintf("%s (%s)", item.Name, reason))
			}
		}

		macros, ok, err := GetItemMacros(db, *ri.ItemID)
		if err != nil {
			return RecipeMacroResult{}, err
		}
		if !ok {
			unknown("no nutrition data")
			continue
		}
		grams, ok, err := ConvertQuantity(db, ri.ItemID, *ri.Quantity, *ri.Unit, "g")
		if err != nil {
			return RecipeMacroResult{}, err
		}
		if !ok {
			unknown(fmt.Sprintf("no %s -> g conversion", CanonicalUnit(*ri.Unit)))
			continue
		}
		scale := grams / 100
		result.Total.Calories += macros.CaloriesPer100g * scale
		result.Total.Protein += macros.ProteinPer100g * scale
		result.Total.Carbs += macros.CarbsPer100g * scale
		result.Total.Fat += macros.FatPer100g * scale
		result.Total.Fiber += macros.FiberPer100g * scale
	}
	return result, nil
}

// RecipeMacroResolution is the outcome of deciding what macros to show or
// charge for one serving of a recipe, in the same three states the cost side uses:
// a manual recipe_meta value always wins; "computed" only applies once the
// ingredient mapping is complete AND every mapped ingredient contributed;
// otherwise the macros are honestly unknown rather than a number that looks
// authoritative but silently omits half the recipe.
type RecipeMacroResolution struct {
	PerServing store.MacrosPerServing
	// Total is the whole batch, which is computable even when the recipe's
	// serving count isn't - worth reporting rather than throwing away.
	Total  store.MacrosPerServing
	Source string // "manual", "computed", or "unknown"
	Reason string // why, when Source is "unknown"
	// ComputedPerServing is what the ingredients say, filled in even when a
	// manual value wins, so a hand-entered figure that disagrees with the
	// recipe's own contents can be noticed instead of quietly standing.
	ComputedPerServing *store.MacrosPerServing
	UnknownItems       []string
}

// ResolveRecipeMacros is the single resolution point `meal recipe show` and
// `meal cook` share, so what's displayed and what gets logged never disagree.
func ResolveRecipeMacros(db *sql.DB, recipeID string, meta store.RecipeMeta, servings float64) (RecipeMacroResolution, error) {
	manual := meta.MacrosPerServing
	// A manual value always wins, but the computed one is still worth
	// having for comparison, so the computation runs either way.
	manualOr := func(res RecipeMacroResolution) RecipeMacroResolution {
		if manual == nil {
			return res
		}
		out := RecipeMacroResolution{PerServing: *manual, Source: "manual", Total: res.Total}
		if res.Source == "computed" {
			computed := res.PerServing
			out.ComputedPerServing = &computed
		}
		return out
	}

	recipe, err := store.GetRecipe(db, recipeID)
	if err != nil {
		return RecipeMacroResolution{}, err
	}
	existing, err := store.ListRecipeIngredients(db, recipeID)
	if err != nil {
		return RecipeMacroResolution{}, err
	}
	if ComputeMappingStatus(FlattenIngredientLines(recipe.IngredientGroups), existing).State != "complete" {
		return manualOr(RecipeMacroResolution{Source: "unknown", Reason: "ingredient mapping is incomplete"}), nil
	}

	result, err := ComputeRecipeMacros(db, recipeID)
	if err != nil {
		return RecipeMacroResolution{}, err
	}
	if !result.AllKnown {
		return manualOr(RecipeMacroResolution{Source: "unknown", Reason: "some ingredients contributed nothing", UnknownItems: result.UnknownItems}), nil
	}
	// The batch total stands on its own; only dividing it into servings
	// needs a serving count, so a missing one costs the per-serving figure
	// rather than the whole computation.
	if servings <= 0 {
		return manualOr(RecipeMacroResolution{
			Total:  result.Total,
			Source: "unknown",
			Reason: "recipe has no known serving count (set one with `meal recipe set-meta <slug> --serves N`)",
		}), nil
	}
	return manualOr(RecipeMacroResolution{
		PerServing: store.MacrosPerServing{
			Calories: result.Total.Calories / servings,
			Protein:  result.Total.Protein / servings,
			Carbs:    result.Total.Carbs / servings,
			Fat:      result.Total.Fat / servings,
			Fiber:    result.Total.Fiber / servings,
		},
		Total:  result.Total,
		Source: "computed",
	}), nil
}

// DepletedItemMacros sums macros for a set of pantry quantities actually
// taken out of stock - the ad-hoc counterpart to ComputeRecipeMacros.
//
// A `meal log --deplete-pantry` entry names exactly what was eaten and in
// what quantity, which is all the same machinery a recipe needs: convert
// each quantity to grams, scale the item's per-100g macros. Source is
// "estimated" when any contributing item's own nutrition was an estimate,
// so an entry never claims more authority than its weakest input.
func DepletedItemMacros(db *sql.DB, items map[int64]struct {
	Qty  float64
	Unit string
}) (macros store.MacrosPerServing, source string, unknown []string, err error) {
	source = "usda"
	for itemID, q := range items {
		name := fmt.Sprintf("item %d", itemID)
		if item, ierr := store.GetPantryItem(db, itemID); ierr == nil {
			name = item.Name
		}
		itemMacros, ok, err := GetItemMacros(db, itemID)
		if err != nil {
			return store.MacrosPerServing{}, "", nil, err
		}
		if !ok {
			unknown = append(unknown, fmt.Sprintf("%s (no nutrition data)", name))
			continue
		}
		grams, ok, err := ConvertQuantity(db, &itemID, q.Qty, q.Unit, "g")
		if err != nil {
			return store.MacrosPerServing{}, "", nil, err
		}
		if !ok {
			unknown = append(unknown, fmt.Sprintf("%s (no %s -> g conversion)", name, CanonicalUnit(q.Unit)))
			continue
		}
		if label, ok, lerr := store.GetItemNutrition(db, itemID); lerr == nil && ok && label.Source == "estimated" {
			source = "estimated"
		}
		scale := grams / 100
		macros.Calories += itemMacros.CaloriesPer100g * scale
		macros.Protein += itemMacros.ProteinPer100g * scale
		macros.Carbs += itemMacros.CarbsPer100g * scale
		macros.Fat += itemMacros.FatPer100g * scale
		macros.Fiber += itemMacros.FiberPer100g * scale
	}
	return macros, source, unknown, nil
}

// ComputeCookEventMacros resolves per-serving macros for one cook event from
// what that cook actually consumed, rather than from the recipe in the
// abstract.
//
// This is the right basis whenever a cook event exists, and the two can
// differ a lot: cook_events records the servings the batch really yielded
// (which may be nothing like the recipe's stated serves), and
// cook_event_ingredients records the quantities really used, including
// --scale, per-ingredient overrides, and any accepted shortfall. Dividing
// the recipe's nominal total by the recipe's nominal serves would have a
// serving eaten at the table and the same serving eaten as a leftover
// disagreeing about what it contained.
//
// Reports unknown when the event didn't consume every ingredient the recipe
// maps - that means the pantry couldn't cover it and the shortfall was
// accepted, so the recorded ingredients are not the whole dish.
func ComputeCookEventMacros(db *sql.DB, cookEventID int64) (perServing store.MacrosPerServing, source string, unknown []string, err error) {
	event, err := store.GetCookEvent(db, cookEventID)
	if err != nil {
		return store.MacrosPerServing{}, "", nil, err
	}
	if event.ServingsYielded <= 0 {
		return store.MacrosPerServing{}, "", []string{"cook event recorded no servings yielded"}, nil
	}

	consumed, err := store.ListCookEventIngredients(db, cookEventID)
	if err != nil {
		return store.MacrosPerServing{}, "", nil, err
	}
	if len(consumed) == 0 {
		return store.MacrosPerServing{}, "", []string{"cook event recorded no ingredient consumption"}, nil
	}

	// Several lots of the same item come back as separate rows; they're one
	// ingredient as far as macros are concerned.
	byItem := map[int64]struct {
		Qty  float64
		Unit string
	}{}
	for _, c := range consumed {
		existing, seen := byItem[c.ItemID]
		if !seen {
			byItem[c.ItemID] = struct {
				Qty  float64
				Unit string
			}{Qty: c.Quantity, Unit: c.Unit}
			continue
		}
		add := c.Quantity
		if CanonicalUnit(c.Unit) != CanonicalUnit(existing.Unit) {
			converted, ok, cerr := ConvertQuantity(db, &c.ItemID, c.Quantity, c.Unit, existing.Unit)
			if cerr != nil {
				return store.MacrosPerServing{}, "", nil, cerr
			}
			if !ok {
				unknown = append(unknown, fmt.Sprintf("item %d was drawn from lots in units that can't be added together", c.ItemID))
				continue
			}
			add = converted
		}
		existing.Qty += add
		byItem[c.ItemID] = existing
	}

	perServing, source, gaps, err := PerServingFromConsumption(db, event.RecipeID, byItem, event.ServingsYielded)
	return perServing, source, append(unknown, gaps...), err
}

// PerServingFromConsumption divides what a batch actually consumed by the
// servings it actually yielded. Shared by `meal cook` (which has the
// consumption in hand, mid-transaction) and ComputeCookEventMacros (which
// reads it back afterwards) so the figure a serving gets at the table and
// the one it gets as a leftover are computed identically.
func PerServingFromConsumption(db *sql.DB, recipeID string, byItem map[int64]struct {
	Qty  float64
	Unit string
}, servings float64) (perServing store.MacrosPerServing, source string, unknown []string, err error) {
	if servings <= 0 {
		return store.MacrosPerServing{}, "", []string{"no serving count for this batch"}, nil
	}

	// An ingredient the recipe maps but the cook never consumed means the
	// pantry couldn't cover it and the shortfall was accepted: what was
	// recorded is not the whole dish.
	mapped, err := store.ListRecipeIngredients(db, recipeID)
	if err != nil {
		return store.MacrosPerServing{}, "", nil, err
	}
	for _, ri := range mapped {
		if ri.Status != "mapped" || *ri.Quantity == 0 {
			continue
		}
		if _, ok := byItem[*ri.ItemID]; !ok {
			name := fmt.Sprintf("item %d", *ri.ItemID)
			if item, ierr := store.GetPantryItem(db, *ri.ItemID); ierr == nil {
				name = item.Name
			}
			unknown = append(unknown, fmt.Sprintf("%s (not consumed by this cook)", name))
		}
	}

	total, source, gaps, err := DepletedItemMacros(db, byItem)
	if err != nil {
		return store.MacrosPerServing{}, "", nil, err
	}
	unknown = append(unknown, gaps...)
	if len(unknown) > 0 {
		return store.MacrosPerServing{}, source, unknown, nil
	}
	return store.MacrosPerServing{
		Calories: total.Calories / servings,
		Protein:  total.Protein / servings,
		Carbs:    total.Carbs / servings,
		Fat:      total.Fat / servings,
		Fiber:    total.Fiber / servings,
	}, source, nil, nil
}
