package dashboard

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jpatrickb/meal-planning/internal/domain"
	"github.com/jpatrickb/meal-planning/internal/store"
)

// recipeSiteBase is where a planned recipe's full instructions live. Recipe
// content is owned by that site, not this database, so the plan page links out
// rather than duplicating ingredients and steps.
const recipeSiteBase = "https://recipes.patrickandthea.com/recipes/"

// planLookaheadDays is the minimum window the plan page always renders, even
// when later days are empty, so an unplanned stretch is visible as a gap
// rather than silently absent.
const planLookaheadDays = 7

// planHorizonDays bounds how far past the lookahead window a planned meal will
// still pull the page's range forward.
const planHorizonDays = 28

// slotOrder is the fixed display order for a day's meal slots.
var slotOrder = []string{"breakfast", "lunch", "dinner", "snack"}

// PlannedMeal is one meal slot as the plan page displays it.
type PlannedMeal struct {
	Slot        string
	Title       string
	Emoji       string
	RecipeURL   string // empty for free-text meals
	PlannedFor  string
	IsFreeText  bool
	IsLeftovers bool
	Status      string
	// Macros is the recipe's per-serving figure, shown so a planned meal
	// can be judged before it's cooked rather than only after it's logged.
	// Empty when the recipe can't resolve macros, or for a free-text slot
	// that isn't tied to a recipe at all.
	Macros string
}

// PlanDay is one calendar day's worth of planned slots.
type PlanDay struct {
	Date      string // YYYY-MM-DD
	Weekday   string // "Saturday"
	DateLabel string // "Aug 22"
	Relative  string // "Today", "Tomorrow", or ""
	IsToday   bool
	IsPast    bool
	Meals     []PlannedMeal
}

// Empty reports whether the day has nothing planned, which the template
// renders as an explicit gap.
func (d PlanDay) Empty() bool { return len(d.Meals) == 0 }

// PlanPageData backs the Meal Plan page.
type PlanPageData struct {
	GeneratedAt string
	Days        []PlanDay
	RangeLabel  string
	TotalMeals  int
	RecipeMeals int
}

// GetPlanPageData assembles the meal plan page: every day from today through
// the last planned date (at least a week out), each with its slots in
// breakfast/lunch/dinner/snack order.
func GetPlanPageData(db *sql.DB) (PlanPageData, error) {
	now := time.Now()
	today := now.Format("2006-01-02")
	from := today
	to := now.AddDate(0, 0, planHorizonDays).Format("2006-01-02")

	entries, err := store.ListPlanEntries(db, from, to)
	if err != nil {
		return PlanPageData{}, err
	}

	// The rendered range runs to the last planned day, but never stops short
	// of the lookahead window.
	lastDay := now.AddDate(0, 0, planLookaheadDays-1)
	for _, e := range entries {
		if d, err := time.ParseInLocation("2006-01-02", e.PlanDate, now.Location()); err == nil && d.After(lastDay) {
			lastDay = d
		}
	}

	byDate := map[string][]store.MealPlanEntry{}
	for _, e := range entries {
		byDate[e.PlanDate] = append(byDate[e.PlanDate], e)
	}

	data := PlanPageData{GeneratedAt: now.Format("Jan 2, 2006 3:04pm")}
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	for d := startOfToday; !d.After(lastDay); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		day := PlanDay{
			Date:      key,
			Weekday:   d.Format("Monday"),
			DateLabel: d.Format("Jan 2"),
			IsToday:   key == today,
		}
		switch int(d.Sub(startOfToday).Hours() / 24) {
		case 0:
			day.Relative = "Today"
		case 1:
			day.Relative = "Tomorrow"
		}

		dayEntries := byDate[key]
		sort.SliceStable(dayEntries, func(i, j int) bool {
			return slotRank(dayEntries[i].MealSlot) < slotRank(dayEntries[j].MealSlot)
		})
		for _, e := range dayEntries {
			day.Meals = append(day.Meals, buildPlannedMeal(db, e))
			data.TotalMeals++
			if e.RecipeID != nil {
				data.RecipeMeals++
			}
		}
		data.Days = append(data.Days, day)
	}

	if len(data.Days) > 0 {
		first, last := data.Days[0], data.Days[len(data.Days)-1]
		data.RangeLabel = first.DateLabel + " to " + last.DateLabel
	}
	return data, nil
}

func buildPlannedMeal(db *sql.DB, e store.MealPlanEntry) PlannedMeal {
	m := PlannedMeal{
		Slot:       e.MealSlot,
		Emoji:      e.RecipeEmoji,
		PlannedFor: e.PlannedFor,
		Status:     e.Status,
	}
	switch {
	case e.RecipeID != nil:
		m.Title = e.RecipeTitle
		m.RecipeURL = recipeSiteBase + *e.RecipeID + "/"
		m.Macros = perServingMacroLabel(db, *e.RecipeID)
	case e.FreeTextMeal != nil:
		m.Title = *e.FreeTextMeal
		m.IsFreeText = true
		// Free-text slots are how leftovers and no-recipe meals get planned;
		// flagging them lets the template mark a zero-cook day at a glance.
		m.IsLeftovers = strings.Contains(strings.ToLower(m.Title), "leftover")
	default:
		m.Title = "(nothing planned)"
		m.IsFreeText = true
	}
	if m.Emoji == "" && m.IsFreeText {
		m.Emoji = "🍽️"
	}
	return m
}

// perServingMacroLabel resolves a recipe's per-serving macros for display,
// returning "" when they're unknown. A plan page is read in the kitchen and
// on a phone, so this is deliberately one short line rather than a table.
func perServingMacroLabel(db *sql.DB, recipeID string) string {
	meta, err := store.GetRecipeMeta(db, recipeID)
	if err != nil {
		return ""
	}
	recipe, err := store.GetRecipe(db, recipeID)
	if err != nil {
		return ""
	}
	servings, _ := domain.ResolveServings(nil, meta.ServesOverride, recipe.ServesRaw, recipeID)
	res, err := domain.ResolveRecipeMacros(db, recipeID, meta, float64(servings))
	if err != nil || res.Source == "unknown" {
		return ""
	}
	m := res.PerServing
	return fmt.Sprintf("%.0f cal · %.0fg protein · %.0fg carbs · %.0fg fat per serving", m.Calories, m.Protein, m.Carbs, m.Fat)
}

func slotRank(slot string) int {
	for i, s := range slotOrder {
		if s == slot {
			return i
		}
	}
	return len(slotOrder)
}
