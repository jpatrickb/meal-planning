// Package domain holds pure logic with no CLI/DB imports: serving-count
// resolution, the leftover forecast, and macro-source fallbacks. Kept
// separate from internal/store so it stays trivially unit-testable.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrServesUnknown is returned when a recipe's serving count can't be
// resolved from any source. Callers should surface this to the user rather
// than guessing - the whole leftover ledger depends on knowing this number.
type ErrServesUnknown struct {
	RecipeID string
}

func (e ErrServesUnknown) Error() string {
	return fmt.Sprintf("serves unknown for recipe %q: set it with `meal recipe set-meta %s --serves N`", e.RecipeID, e.RecipeID)
}

var rangeRe = regexp.MustCompile(`(\d+)\s*[-\x{2013}\x{2014}]\s*(\d+)`)
var singleRe = regexp.MustCompile(`\d+`)

// ParseServesRaw best-effort parses the site's free-text `serves` field
// ("4", "4-6", "4–6", "12 servings") into a whole number of servings.
// Ranges are averaged and rounded to the nearest integer. Returns ok=false
// (never an error) when raw is empty or has no parseable number - the
// distinction between "empty" and "unparseable" doesn't matter to callers.
func ParseServesRaw(raw string) (servings int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if m := rangeRe.FindStringSubmatch(raw); m != nil {
		lo, err1 := strconv.Atoi(m[1])
		hi, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && hi >= lo {
			return int(roundHalfUp(float64(lo+hi) / 2)), true
		}
	}
	if m := singleRe.FindString(raw); m != "" {
		n, err := strconv.Atoi(m)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func roundHalfUp(f float64) float64 {
	if f < 0 {
		return -roundHalfUp(-f)
	}
	return float64(int(f + 0.5))
}

// ResolveServings determines how many servings a cook event yields, in
// strict precedence order: an explicit override for this invocation, then a
// previously-saved recipe_meta override, then a best-effort parse of the
// site's serves_raw string. Returns ErrServesUnknown if none resolve -
// callers must not fall back to guessing.
func ResolveServings(invocationOverride *int, savedOverride *int, servesRaw, recipeID string) (int, error) {
	if invocationOverride != nil {
		return *invocationOverride, nil
	}
	if savedOverride != nil {
		return *savedOverride, nil
	}
	if n, ok := ParseServesRaw(servesRaw); ok {
		return n, nil
	}
	return 0, ErrServesUnknown{RecipeID: recipeID}
}
