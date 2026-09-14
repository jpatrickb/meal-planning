package domain

import "github.com/jpatrickb/meal-planning/internal/store"

// IngredientLine is one raw ingredient line from a recipe's ingredient
// groups, flattened into a single ordered, 1-indexed list for CLI addressing
// (`meal recipe map-ingredients <slug> --line N`).
type IngredientLine struct {
	Index   int
	Group   *string
	RawText string
}

// FlattenIngredientLines flattens a recipe's ingredient groups into an
// ordered, 1-indexed list. Order matches the site's JSON, so line numbers
// stay stable across calls as long as the recipe hasn't been re-synced with
// different content (see stale-mapping detection, docs §8 phase 5).
func FlattenIngredientLines(groups []store.IngredientGroup) []IngredientLine {
	var out []IngredientLine
	i := 1
	for _, g := range groups {
		for _, item := range g.Items {
			out = append(out, IngredientLine{Index: i, Group: g.Heading, RawText: item})
			i++
		}
	}
	return out
}

// MappingStatus summarizes how complete a recipe's ingredient mapping is,
// in one of three states: unmapped (no rows at all), partial (some lines accounted for), or complete
// (every line mapped or explicitly not_tracked).
type MappingStatus struct {
	State         string // "unmapped", "partial", "complete"
	TotalLines    int
	ResolvedLines int
	Unresolved    []IngredientLine
}

// ComputeMappingStatus matches existing recipe_ingredients rows against a
// recipe's current ingredient lines (by group + raw text) and derives the
// three-state completeness. A line whose text no longer matches any row --
// because the recipe was re-synced with different content -- counts as
// unresolved, which is exactly the stale-mapping signal docs §8 phase 5
// describes; this function is what a future `meal sync recipes` stale check
// would call too.
func ComputeMappingStatus(lines []IngredientLine, existing []store.RecipeIngredient) MappingStatus {
	resolved := map[string]bool{}
	for _, ri := range existing {
		resolved[ingredientKey(ri.IngredientGroup, ri.RawIngredientText)] = true
	}

	status := MappingStatus{TotalLines: len(lines)}
	for _, l := range lines {
		if resolved[ingredientKey(l.Group, l.RawText)] {
			status.ResolvedLines++
		} else {
			status.Unresolved = append(status.Unresolved, l)
		}
	}

	switch {
	case status.TotalLines == 0:
		status.State = "complete" // no ingredient lines at all -- vacuously complete
	case status.ResolvedLines == 0:
		status.State = "unmapped"
	case status.ResolvedLines == status.TotalLines:
		status.State = "complete"
	default:
		status.State = "partial"
	}
	return status
}

func ingredientKey(group *string, rawText string) string {
	g := ""
	if group != nil {
		g = *group
	}
	return g + "\x00" + rawText
}
