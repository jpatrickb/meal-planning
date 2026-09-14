package domain

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jpatrickb/meal-planning/internal/store"
	"github.com/jpatrickb/meal-planning/internal/usda"
)

// ResolvedMacros is per-100g macro data plus where it came from. Source is
// always "usda" or "estimated" here - "local_meta" is only ever set by
// callers that already have a recipe_meta match (cook.go/log.go), not by
// this resolver.
type ResolvedMacros struct {
	CaloriesPer100g float64
	ProteinPer100g  float64
	CarbsPer100g    float64
	FatPer100g      float64
	FiberPer100g    float64
	Source          string
	Description     string
}

// NutritionCandidateThreshold is the minimum trigram-Jaccard similarity for
// an existing nutrition query to be surfaced as a "did you mean" candidate.
// Deliberately lower than the item-alias threshold: natural-language food
// descriptions share more incidental trigrams (articles, spacing) than
// abbreviated receipt text, and a false-positive candidate here costs one
// glance, not corrupted data - so it's fine to be generous about surfacing.
const NutritionCandidateThreshold = 0.2

// MaxNutritionCandidates caps how many near-matches get surfaced.
const MaxNutritionCandidates = 5

// MacroCandidate is a near-match: NOT a resolution, just something to
// consider. Similarity alone can't tell "grilled chicken breast" from "fried
// chicken breast" apart - only a real judgment call can, which is exactly
// why this is surfaced rather than auto-applied.
type MacroCandidate struct {
	FDCID       int
	QueryText   string
	Description string
	Similarity  float64
}

// MacroResolution is the outcome of ResolveFoodMacros: either Resolved is
// set (safe to use directly - exact match, fresh USDA lookup, or estimate),
// or Candidates is set and the caller must explicitly choose via
// ResolveFoodMacrosByFDCID or ForceFreshFoodMacros before anything is logged.
type MacroResolution struct {
	Resolved   *ResolvedMacros
	Candidates []MacroCandidate
}

// ResolveFoodMacros resolves per-100g macros for a free-text food query:
//  1. Exact match against a previously confirmed query -> instant reuse, no
//     API call, no candidates (this exact text was already settled before).
//  2. No exact match: fuzzy-search prior confirmed queries. Any near-match
//     blocks automatic resolution and comes back as Candidates - the caller
//     must decide (see ResolveFoodMacrosByFDCID / ForceFreshFoodMacros)
//     rather than have the tool guess whether it's really the same food.
//  3. Nothing even close: proceed automatically (live USDA search, cached
//     after and self-confirmed; falling back to a crude keyword estimate if
//     USDA is unavailable or errors - a degraded guess beats blocking a log
//     entry when there was nothing plausible to confuse it with anyway).
func ResolveFoodMacros(ctx context.Context, db *sql.DB, usdaClient *usda.Client, query string) (MacroResolution, error) {
	normalized := normalizeQuery(query)

	if fdcID, ok, err := store.GetNutritionAliasExact(db, normalized); err != nil {
		return MacroResolution{}, err
	} else if ok {
		resolved, err := resolveByFDCID(db, fdcID)
		if err != nil {
			return MacroResolution{}, err
		}
		return MacroResolution{Resolved: &resolved}, nil
	}

	candidates, err := store.ListNutritionCandidates(db)
	if err != nil {
		return MacroResolution{}, err
	}
	var matches []MacroCandidate
	for _, c := range candidates {
		if sim := JaccardSimilarity(normalized, c.QueryText); sim >= NutritionCandidateThreshold {
			matches = append(matches, MacroCandidate{FDCID: c.FDCID, QueryText: c.QueryText, Description: c.Description, Similarity: sim})
		}
	}
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].Similarity > matches[j].Similarity })
		if len(matches) > MaxNutritionCandidates {
			matches = matches[:MaxNutritionCandidates]
		}
		return MacroResolution{Candidates: matches}, nil
	}

	resolved, err := freshFoodMacros(ctx, db, usdaClient, query, normalized)
	if err != nil {
		return MacroResolution{}, err
	}
	return MacroResolution{Resolved: &resolved}, nil
}

// ResolveFoodMacrosByFDCID is the explicit "yes, this candidate is the same
// food" path: fetches the canonical entry and confirms newQueryText as an
// alias for it, so the identical text resolves instantly (no candidates)
// next time.
func ResolveFoodMacrosByFDCID(db *sql.DB, fdcID int, newQueryText string) (ResolvedMacros, error) {
	resolved, err := resolveByFDCID(db, fdcID)
	if err != nil {
		return ResolvedMacros{}, err
	}
	if err := store.UpsertNutritionAlias(db, normalizeQuery(newQueryText), fdcID); err != nil {
		return ResolvedMacros{}, err
	}
	return resolved, nil
}

// ForceFreshFoodMacros is the explicit "no, none of those candidates are
// actually this food" path: skips matching entirely and does a fresh
// USDA/estimate lookup, self-confirmed under this exact query text.
func ForceFreshFoodMacros(ctx context.Context, db *sql.DB, usdaClient *usda.Client, query string) (ResolvedMacros, error) {
	return freshFoodMacros(ctx, db, usdaClient, query, normalizeQuery(query))
}

func resolveByFDCID(db *sql.DB, fdcID int) (ResolvedMacros, error) {
	cached, ok, err := store.GetNutritionByFDCID(db, fdcID)
	if err != nil {
		return ResolvedMacros{}, err
	}
	if !ok {
		return ResolvedMacros{}, fmt.Errorf("fdc_id %d not found in nutrition_cache", fdcID)
	}
	return ResolvedMacros{
		CaloriesPer100g: cached.CaloriesPer100g, ProteinPer100g: cached.ProteinPer100g,
		CarbsPer100g: cached.CarbsPer100g, FatPer100g: cached.FatPer100g, FiberPer100g: cached.FiberPer100g,
		Source: "usda", Description: cached.Description,
	}, nil
}

func freshFoodMacros(ctx context.Context, db *sql.DB, usdaClient *usda.Client, query, normalized string) (ResolvedMacros, error) {
	if usdaClient != nil {
		food, err := usdaClient.SearchFoods(ctx, query)
		if err == nil && food != nil {
			entry := store.NutritionCacheEntry{
				FDCID: food.FDCID, Description: food.Description, QueryText: normalized,
				CaloriesPer100g: food.CaloriesPer100g, ProteinPer100g: food.ProteinPer100g,
				CarbsPer100g: food.CarbsPer100g, FatPer100g: food.FatPer100g, FiberPer100g: food.FiberPer100g,
				RawJSON: food.RawJSON, FetchedAt: time.Now().UTC().Format(time.RFC3339),
			}
			if err := store.UpsertNutritionCache(db, entry); err != nil {
				return ResolvedMacros{}, err
			}
			if err := store.UpsertNutritionAlias(db, normalized, food.FDCID); err != nil {
				return ResolvedMacros{}, err
			}
			return ResolvedMacros{
				CaloriesPer100g: food.CaloriesPer100g, ProteinPer100g: food.ProteinPer100g,
				CarbsPer100g: food.CarbsPer100g, FatPer100g: food.FatPer100g, FiberPer100g: food.FiberPer100g,
				Source: "usda", Description: food.Description,
			}, nil
		}
		// USDA errored or found nothing: fall through to the estimate below.
	}

	cal, pro, carb, fat, fib, desc := EstimateMacros(query)
	return ResolvedMacros{
		CaloriesPer100g: cal, ProteinPer100g: pro, CarbsPer100g: carb, FatPer100g: fat, FiberPer100g: fib,
		Source: "estimated", Description: desc,
	}, nil
}

// LinkPantryItemNutrition links a pantry item to a real USDA food entry, for
// computed macros later. Unlike ResolveFoodMacros (used for free-text
// consumption log entries), this never blocks on a near-match candidate for
// confirmation - Patrick's call: a pantry item already has a deliberately
// chosen, canonical, generic name (see the bulk-mapping naming convention),
// so picking the best USDA match for it directly is a reasonable default,
// not a risky guess the way matching an ambiguous free-text phrase would
// be. By default searches using the item's own name; pass a non-empty
// queryOverride to search with different text instead (e.g. "milk, whole"
// instead of the bare item name "Milk"), for correcting a bad match found
// after the fact - real testing found USDA's ranking for short generic
// names isn't always reliable even after filtering, so Claude is expected
// to spot-check results and re-link with a better query when needed, not
// treat the first pass as final. Set force to true to re-link an item that
// already has a (wrong) fdc_id. Returns linked=false (not an error) if the
// item is already linked and force is false, no USDA client is configured,
// or nothing matched.
func LinkPantryItemNutrition(ctx context.Context, db *sql.DB, usdaClient *usda.Client, itemID int64, queryOverride string, force bool) (linked bool, description string, err error) {
	item, err := store.GetPantryItem(db, itemID)
	if err != nil {
		return false, "", err
	}
	if (item.FDCID != nil && !force) || usdaClient == nil {
		return false, "", nil
	}

	query := item.Name
	if queryOverride != "" {
		query = queryOverride
	}
	food, err := usdaClient.SearchGenericFoods(ctx, query)
	if err != nil || food == nil {
		return false, "", err
	}

	entry := store.NutritionCacheEntry{
		FDCID: food.FDCID, Description: food.Description, QueryText: normalizeQuery(query),
		CaloriesPer100g: food.CaloriesPer100g, ProteinPer100g: food.ProteinPer100g,
		CarbsPer100g: food.CarbsPer100g, FatPer100g: food.FatPer100g, FiberPer100g: food.FiberPer100g,
		RawJSON: food.RawJSON, FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.UpsertNutritionCache(db, entry); err != nil {
		return false, "", err
	}
	if err := store.SetPantryItemFDCID(db, itemID, food.FDCID); err != nil {
		return false, "", err
	}
	return true, food.Description, nil
}

func normalizeQuery(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// EstimateMacros is the last-resort fallback: crude keyword-category
// defaults per 100g. Deliberately conservative and coarse - only reached
// when neither the local cache nor a live USDA lookup produced anything.
func EstimateMacros(query string) (calories, protein, carbs, fat, fiber float64, categoryLabel string) {
	q := normalizeQuery(query)
	for _, cat := range macroEstimateCategories {
		for _, kw := range cat.keywords {
			if strings.Contains(q, kw) {
				return cat.calories, cat.protein, cat.carbs, cat.fat, cat.fiber, cat.label
			}
		}
	}
	return 150, 5, 15, 7, 1, "generic mixed food (no keyword match)"
}

type macroEstimateCategory struct {
	label                                string
	keywords                             []string
	calories, protein, carbs, fat, fiber float64
}

// Order matters: first matching category wins.
var macroEstimateCategories = []macroEstimateCategory{
	{"lean meat/fish/poultry/egg/tofu", []string{"chicken", "beef", "turkey", "pork", "fish", "salmon", "tuna", "shrimp", "egg", "tofu"}, 165, 25, 0, 6, 0},
	{"grain/starch", []string{"rice", "pasta", "bread", "potato", "oat", "tortilla", "noodle", "cereal"}, 130, 3, 27, 1, 2},
	{"vegetable", []string{"broccoli", "spinach", "carrot", "pepper", "tomato", "onion", "lettuce", "cucumber", "bean", "pea", "zucchini"}, 30, 2, 6, 0.3, 2},
	{"fruit", []string{"apple", "banana", "berry", "orange", "grape", "melon", "peach", "pear"}, 55, 0.5, 14, 0.2, 2},
	{"dairy", []string{"milk", "cheese", "yogurt", "cream", "butter"}, 100, 5, 5, 6, 0},
}

// NormalizeFoodQuery is the canonical form a food's text is stored under in
// nutrition_query_aliases. Exported so a caller holding an already-logged
// entry's free text (rather than a fresh query) can map it back to the food
// that was confirmed for it.
func NormalizeFoodQuery(s string) string {
	return normalizeQuery(s)
}
