package domain

import (
	"database/sql"
	"strings"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// FuzzyMatchThreshold is the minimum trigram-Jaccard similarity for a fuzzy
// match to be surfaced as a pending alias at all. Below this, a line is
// reported unmatched rather than offering a low-confidence guess.
const FuzzyMatchThreshold = 0.35

// NormalizeReceiptText lowercases, trims, and collapses whitespace so the
// same product printed slightly differently ("GV TORTILLA MED", "gv tortilla
// med ") still matches. Deliberately not stripping quantity/size tokens -
// that risks merging genuinely different products; exact matching over a
// slightly noisier string is safer than an over-aggressive normalizer.
func NormalizeReceiptText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func trigrams(s string) map[string]struct{} {
	set := map[string]struct{}{}
	if len(s) < 3 {
		if s != "" {
			set[s] = struct{}{}
		}
		return set
	}
	for i := 0; i+3 <= len(s); i++ {
		set[s[i:i+3]] = struct{}{}
	}
	return set
}

// JaccardSimilarity scores two strings by character-trigram overlap:
// |intersection| / |union|, in [0, 1]. Works reasonably on short, abbreviated
// receipt text where word-boundary tokenizing would be unreliable.
func JaccardSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	intersection := 0
	for g := range ta {
		if _, ok := tb[g]; ok {
			intersection++
		}
	}
	union := len(ta) + len(tb) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// AliasMatchResult is the outcome of matching one receipt line.
type AliasMatchResult struct {
	Method         string // "upc", "exact_alias", "fuzzy", or "unmatched"
	ItemID         *int64
	Confidence     float64
	PendingAliasID *int64 // set only for "fuzzy" - the alias row awaiting confirmation
}

// MatchPurchaseLine resolves a receipt line to a pantry item via, in order:
// UPC exact match, normalized-text exact match, trigram-fuzzy match against
// known items/aliases (creating a pending alias for confirmation, but NOT
// setting ItemID - an unconfirmed guess never auto-stocks), or unmatched.
func MatchPurchaseLine(db *sql.DB, rawText, upc string) (AliasMatchResult, error) {
	normalized := NormalizeReceiptText(rawText)

	if upc != "" {
		if alias, ok, err := store.GetAliasByUPC(db, upc); err != nil {
			return AliasMatchResult{}, err
		} else if ok {
			id := alias.ItemID
			return AliasMatchResult{Method: "upc", ItemID: &id, Confidence: 1.0}, nil
		}
	}

	if alias, ok, err := store.GetAliasByNormalizedText(db, normalized); err != nil {
		return AliasMatchResult{}, err
	} else if ok {
		id := alias.ItemID
		return AliasMatchResult{Method: "exact_alias", ItemID: &id, Confidence: 1.0}, nil
	}

	candidates, err := store.ListMatchCandidates(db)
	if err != nil {
		return AliasMatchResult{}, err
	}
	var bestItemID int64
	var bestScore float64
	found := false
	for _, c := range candidates {
		score := JaccardSimilarity(normalized, NormalizeReceiptText(c.Text))
		if score > bestScore {
			bestScore, bestItemID, found = score, c.ItemID, true
		}
	}
	if found && bestScore >= FuzzyMatchThreshold {
		aliasID, err := store.CreatePendingAlias(db, normalized, bestItemID, bestScore)
		if err != nil {
			return AliasMatchResult{}, err
		}
		return AliasMatchResult{Method: "fuzzy", Confidence: bestScore, PendingAliasID: &aliasID}, nil
	}

	return AliasMatchResult{Method: "unmatched"}, nil
}
