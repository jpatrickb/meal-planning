package dashboard

import (
	"database/sql"
	"strings"
	"time"
	"unicode"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// maxSectionPrefixLen bounds how long an "AISLE: item" prefix may be before it
// is treated as ordinary item text rather than a section marker.
const maxSectionPrefixLen = 24

// ungroupedSection holds items written without a section prefix.
const ungroupedSection = "Everything else"

// ShoppingSection is one store area's worth of list items.
type ShoppingSection struct {
	Name       string
	Items      []ShoppingItem
	TotalCount int
	OpenCount  int
}

// ShoppingItem is one line on the list, with its section prefix stripped.
type ShoppingItem struct {
	Description string
	SourceLabel string // recipe id, or "" for manually added items
	Checked     bool
}

// ShoppingPageData backs the Shopping List page.
type ShoppingPageData struct {
	GeneratedAt string
	Sections    []ShoppingSection
	OpenCount   int
	DoneCount   int
	TotalCount  int
}

// GetShoppingPageData assembles the shopping list grouped by store section.
//
// Sections come from an optional "AISLE: item text" prefix on the item
// description, which is a writing convention rather than a schema column: the
// list is curated by hand for a specific week, so the grouping that helps in
// the store belongs with the text, not in a fixed table of aisles. Items with
// no such prefix collect under a catch-all section instead of being dropped.
func GetShoppingPageData(db *sql.DB) (ShoppingPageData, error) {
	items, err := store.ListShoppingListItems(db, true)
	if err != nil {
		return ShoppingPageData{}, err
	}

	data := ShoppingPageData{GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm")}
	byName := map[string]*ShoppingSection{}
	var order []string

	for _, it := range items {
		section, desc := splitSection(it.Description)
		sec, ok := byName[section]
		if !ok {
			sec = &ShoppingSection{Name: section}
			byName[section] = sec
			order = append(order, section)
		}
		source := ""
		if it.SourceRecipeID != nil {
			source = *it.SourceRecipeID
		}
		sec.Items = append(sec.Items, ShoppingItem{Description: desc, SourceLabel: source, Checked: it.Checked})
		sec.TotalCount++
		data.TotalCount++
		if it.Checked {
			data.DoneCount++
		} else {
			sec.OpenCount++
			data.OpenCount++
		}
	}

	// The catch-all always sorts last so real aisles lead.
	for _, name := range order {
		if name != ungroupedSection {
			data.Sections = append(data.Sections, *byName[name])
		}
	}
	if sec, ok := byName[ungroupedSection]; ok {
		data.Sections = append(data.Sections, *sec)
	}
	return data, nil
}

// splitSection pulls an "AISLE: item" prefix off a description. The prefix
// counts only when it is short and carries no lowercase letters, so an ordinary
// description that happens to contain a colon ("Milk: get the 2%") stays whole.
func splitSection(description string) (section, rest string) {
	idx := strings.Index(description, ": ")
	if idx <= 0 || idx > maxSectionPrefixLen {
		return ungroupedSection, description
	}
	prefix := description[:idx]
	hasLetter := false
	for _, r := range prefix {
		if unicode.IsLower(r) {
			return ungroupedSection, description
		}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
	}
	if !hasLetter {
		return ungroupedSection, description
	}
	return prefix, strings.TrimSpace(description[idx+2:])
}
