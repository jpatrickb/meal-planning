package dashboard

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// categoryLabels turns the stored category codes into something readable.
// Kept here rather than in the store because it's a presentation concern -
// the codes are the real values everywhere else.
var categoryLabels = map[string]string{
	"grains": "Grains", "protein_meat": "Meat", "protein_legume": "Legumes",
	"protein_other": "Other protein", "veg_fresh_frozen": "Vegetables",
	"veg_canned": "Canned vegetables", "fruit": "Fruit", "baking": "Baking",
	"oils_condiments": "Oils & condiments", "sauces": "Sauces", "dairy": "Dairy",
	"misc": "Other",
}

// PantryItemRow is one on-hand item as the pantry page shows it.
type PantryItemRow struct {
	Name        string
	Category    string
	Quantity    float64
	Unit        string
	ExpiresDate string
	DaysLeft    int
	HasExpiry   bool
	// Urgency drives the badge: "expired", "soon" (3 days or less), or "".
	Urgency string
}

// PantryCategoryGroup is one category's worth of items.
type PantryCategoryGroup struct {
	Code  string
	Label string
	Items []PantryItemRow
	Count int
}

// PantryPageData backs the Pantry page.
type PantryPageData struct {
	GeneratedAt string
	Groups      []PantryCategoryGroup
	// UseFirst is the short pinned list at the top: everything expired or
	// within three days. The whole point of tracking expiry is acting on it
	// before it becomes waste, which needs it visible without scrolling.
	UseFirst   []PantryItemRow
	TotalItems int
}

// GetPantryPageData assembles the pantry page: everything on hand, grouped by
// category, with expiry folded in from the expiring-stock view.
func GetPantryPageData(db *sql.DB) (PantryPageData, error) {
	onHand, err := store.ListPantryOnHand(db)
	if err != nil {
		return PantryPageData{}, err
	}
	// No day bound: an already-expired lot matters most of all, and
	// ListExpiringStock includes those when withinDays <= 0.
	expiring, err := store.ListExpiringStock(db, 0)
	if err != nil {
		return PantryPageData{}, err
	}

	type expiryInfo struct {
		date string
		days int
	}
	soonest := map[string]expiryInfo{}
	for _, e := range expiring {
		if cur, seen := soonest[e.ItemName]; !seen || e.DaysUntilExpiry < cur.days {
			soonest[e.ItemName] = expiryInfo{date: e.ExpiresDate, days: e.DaysUntilExpiry}
		}
	}

	byCategory := map[string][]PantryItemRow{}
	var useFirst []PantryItemRow
	for _, p := range onHand {
		row := PantryItemRow{Name: p.Name, Category: p.Category, Quantity: p.Quantity, Unit: p.Unit}
		if info, ok := soonest[p.Name]; ok {
			row.ExpiresDate, row.DaysLeft, row.HasExpiry = info.date, info.days, true
			switch {
			case info.days < 0:
				row.Urgency = "expired"
			case info.days <= 3:
				row.Urgency = "soon"
			}
		}
		byCategory[p.Category] = append(byCategory[p.Category], row)
	}

	// Built from the expiring LOTS, not from item totals: an item can have
	// one old lot and plenty of fresh stock, and saying "7.50 count past
	// its date" when one banana is old overstates the problem enough that
	// the list stops being believed.
	for _, e := range expiring {
		if e.DaysUntilExpiry > 3 {
			continue
		}
		urgency := "soon"
		if e.DaysUntilExpiry < 0 {
			urgency = "expired"
		}
		useFirst = append(useFirst, PantryItemRow{
			Name: e.ItemName, Category: e.Category, Quantity: e.Quantity, Unit: e.Unit,
			ExpiresDate: e.ExpiresDate, DaysLeft: e.DaysUntilExpiry, HasExpiry: true, Urgency: urgency,
		})
	}
	sort.Slice(useFirst, func(i, j int) bool { return useFirst[i].DaysLeft < useFirst[j].DaysLeft })

	groups := make([]PantryCategoryGroup, 0, len(byCategory))
	for code, items := range byCategory {
		label := categoryLabels[code]
		if label == "" {
			label = strings.ReplaceAll(code, "_", " ")
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		groups = append(groups, PantryCategoryGroup{Code: code, Label: label, Items: items, Count: len(items)})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })

	return PantryPageData{
		GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm"),
		Groups:      groups,
		UseFirst:    useFirst,
		TotalItems:  len(onHand),
	}, nil
}
