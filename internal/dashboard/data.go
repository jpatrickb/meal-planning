// Package dashboard renders the SQLite data into a static HTML site:
// server-rendered Go templates + inline SVG charts, no JS, no build step.
package dashboard

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// WeeklyCost is one week's household cost, split by person, for the cost trend chart.
type WeeklyCost struct {
	Week          string
	PatrickCost   float64
	TheaCost      float64
	HouseholdCost float64
}

// GetWeeklyCostTrend returns the last `weeks` weeks of cost, oldest first.
func GetWeeklyCostTrend(db *sql.DB, weeks int) ([]WeeklyCost, error) {
	since := time.Now().AddDate(0, 0, -7*weeks).Format("2006-01-02")
	rows, err := db.Query(`
		SELECT strftime('%Y-W%W', consumed_date) AS week, person, SUM(cost)
		FROM consumption_log
		WHERE consumed_date >= ?
		GROUP BY week, person
		ORDER BY week
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byWeek := map[string]*WeeklyCost{}
	var order []string
	for rows.Next() {
		var week, person string
		var cost float64
		if err := rows.Scan(&week, &person, &cost); err != nil {
			return nil, err
		}
		w, ok := byWeek[week]
		if !ok {
			w = &WeeklyCost{Week: week}
			byWeek[week] = w
			order = append(order, week)
		}
		if person == "patrick" {
			w.PatrickCost = cost
		} else {
			w.TheaCost = cost
		}
		w.HouseholdCost += cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]WeeklyCost, 0, len(order))
	for _, week := range order {
		out = append(out, *byWeek[week])
	}
	return out, nil
}

// HomeData backs the dashboard's landing page: what's needed right now.
//
// Deliberately not everything. The full pantry lives on its own page; a
// 65-row table here buried the three things actually worth opening the page
// for - what's being eaten today, what needs acting on, and how the week is
// tracking.
type HomeData struct {
	GeneratedAt   string
	Today         TodayView
	ExpiringSoon  []store.ExpiringStock
	ShoppingList  []store.ShoppingListItem
	OpenLeftovers []store.CookEventDetail
	Macros        store.MacroAnalytics
	Alerts        []HomeAlert
}

// TodayView is today's eating, logged and planned together.
type TodayView struct {
	DateLabel string
	Weekday   string
	People    []PersonDayTotals
	Slots     []TodaySlot
}

// PersonDayTotals is one person's running total for today.
type PersonDayTotals struct {
	Person   string
	Calories float64
	Protein  float64
	Carbs    float64
	Fat      float64
	Meals    int
}

// TodaySlot is one meal slot, showing what was eaten if anything was logged
// and what was planned otherwise - the difference between the two is most of
// what someone checks a dashboard mid-day to find out.
type TodaySlot struct {
	Slot    string
	Entries []TodaySlotEntry
	Planned string
	IsPlan  bool
}

// TodaySlotEntry is one logged meal within a slot.
type TodaySlotEntry struct {
	Person    string
	Food      string
	Calories  float64
	Protein   float64
	HasMacros bool
}

// HomeAlert is one thing worth acting on, ranked most urgent first.
type HomeAlert struct {
	Urgency string // "critical", "warning", or "info"
	Text    string
}

// GetHomeData assembles the home page's data.
func GetHomeData(db *sql.DB) (HomeData, error) {
	today := time.Now().Format("2006-01-02")
	weekOut := time.Now().AddDate(0, 0, 6).Format("2006-01-02")

	plan, err := store.ListPlanEntries(db, today, weekOut)
	if err != nil {
		return HomeData{}, err
	}
	expiring, err := store.ListExpiringStock(db, 7)
	if err != nil {
		return HomeData{}, err
	}
	shopping, err := store.ListShoppingListItems(db, false)
	if err != nil {
		return HomeData{}, err
	}
	leftovers, err := store.ListOpenCookEvents(db)
	if err != nil {
		return HomeData{}, err
	}
	macros, err := store.GetMacroAnalytics(db, time.Now().AddDate(0, 0, -7).Format("2006-01-02"))
	if err != nil {
		return HomeData{}, err
	}
	todayView, err := buildTodayView(db, today, plan)
	if err != nil {
		return HomeData{}, err
	}

	return HomeData{
		GeneratedAt:  time.Now().Format("Jan 2, 2006 3:04pm"),
		Today:        todayView,
		ExpiringSoon: expiring, ShoppingList: shopping, OpenLeftovers: leftovers,
		Macros: macros,
		Alerts: buildHomeAlerts(expiring, leftovers, shopping),
	}, nil
}

// buildTodayView merges what has been logged today with what was planned for
// it, slot by slot.
func buildTodayView(db *sql.DB, today string, plan []store.MealPlanEntry) (TodayView, error) {
	entries, err := store.ListConsumptionEntries(db, store.ConsumptionListOpts{DateFrom: today, DateTo: today})
	if err != nil {
		return TodayView{}, err
	}

	totals := map[string]*PersonDayTotals{}
	bySlot := map[string][]TodaySlotEntry{}
	for _, e := range entries {
		t, ok := totals[e.Person]
		if !ok {
			t = &PersonDayTotals{Person: e.Person}
			totals[e.Person] = t
		}
		t.Calories += derefFloat(e.Calories)
		t.Protein += derefFloat(e.Protein)
		t.Carbs += derefFloat(e.Carbs)
		t.Fat += derefFloat(e.Fat)
		t.Meals++

		food := "-"
		switch {
		case e.FreeTextFood != nil && *e.FreeTextFood != "":
			food = *e.FreeTextFood
		case e.RecipeID != nil:
			food = *e.RecipeID
			if r, rerr := store.GetRecipe(db, *e.RecipeID); rerr == nil {
				food = r.Title
			}
		}
		slot := "other"
		if e.MealSlot != nil {
			slot = *e.MealSlot
		}
		bySlot[slot] = append(bySlot[slot], TodaySlotEntry{
			Person: e.Person, Food: food, Calories: derefFloat(e.Calories),
			Protein: derefFloat(e.Protein), HasMacros: e.Calories != nil,
		})
	}

	plannedBySlot := map[string]string{}
	for _, p := range plan {
		if p.PlanDate != today {
			continue
		}
		switch {
		case p.RecipeTitle != "":
			plannedBySlot[p.MealSlot] = p.RecipeTitle
		case p.FreeTextMeal != nil:
			plannedBySlot[p.MealSlot] = *p.FreeTextMeal
		}
	}

	view := TodayView{}
	if t, terr := time.Parse("2006-01-02", today); terr == nil {
		view.Weekday, view.DateLabel = t.Format("Monday"), t.Format("Jan 2")
	}
	for _, slot := range []string{"breakfast", "lunch", "dinner", "snack"} {
		logged, planned := bySlot[slot], plannedBySlot[slot]
		// Consumption comes back newest-first, which interleaves the two
		// people mid-slot. Grouping by person reads as "what each of us
		// had", which is the question.
		sort.SliceStable(logged, func(i, j int) bool {
			if logged[i].Person != logged[j].Person {
				return logged[i].Person < logged[j].Person
			}
			return logged[i].Food < logged[j].Food
		})
		if len(logged) == 0 && planned == "" {
			continue
		}
		view.Slots = append(view.Slots, TodaySlot{
			Slot: slot, Entries: logged, Planned: planned, IsPlan: len(logged) == 0,
		})
	}
	for _, person := range sortedPeople(totals) {
		view.People = append(view.People, *totals[person])
	}
	return view, nil
}

func sortedPeople(totals map[string]*PersonDayTotals) []string {
	out := make([]string, 0, len(totals))
	for p := range totals {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// buildHomeAlerts turns the raw lists into a short ranked set of things worth
// doing something about. Only genuinely actionable items: a lot expiring in
// six days is not news, and saying so every day trains people to skip the box.
func buildHomeAlerts(expiring []store.ExpiringStock, leftovers []store.CookEventDetail, shopping []store.ShoppingListItem) []HomeAlert {
	var alerts []HomeAlert
	for _, e := range expiring {
		switch {
		case e.DaysUntilExpiry < 0:
			alerts = append(alerts, HomeAlert{"critical", fmt.Sprintf("%s is past its date (%.2f %s)", e.ItemName, e.Quantity, e.Unit)})
		case e.DaysUntilExpiry == 0:
			alerts = append(alerts, HomeAlert{"critical", fmt.Sprintf("%s should be used today (%.2f %s)", e.ItemName, e.Quantity, e.Unit)})
		case e.DaysUntilExpiry <= 2:
			alerts = append(alerts, HomeAlert{"warning", fmt.Sprintf("%s in %d day(s) (%.2f %s)", e.ItemName, e.DaysUntilExpiry, e.Quantity, e.Unit)})
		}
	}
	for _, l := range leftovers {
		if l.DaysSinceCook >= 3 {
			alerts = append(alerts, HomeAlert{"warning",
				fmt.Sprintf("%s leftovers are %d days old (%.0f servings left)", l.RecipeTitle, l.DaysSinceCook, l.ServingsRemaining)})
		}
	}
	if n := len(shopping); n > 0 {
		alerts = append(alerts, HomeAlert{"info", fmt.Sprintf("%d item(s) on the shopping list", n)})
	}
	return alerts
}

// CostPageData backs the Cost & Trends page.
type CostPageData struct {
	GeneratedAt string
	Weekly      []WeeklyCost
	Analytics   store.CostAnalytics
}

// GetCostPageData assembles the cost trends page's data (last 8 weeks).
func GetCostPageData(db *sql.DB) (CostPageData, error) {
	weekly, err := GetWeeklyCostTrend(db, 8)
	if err != nil {
		return CostPageData{}, err
	}
	since := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	analytics, err := store.GetCostAnalytics(db, since)
	if err != nil {
		return CostPageData{}, err
	}
	return CostPageData{GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm"), Weekly: weekly, Analytics: analytics}, nil
}

// MacrosPageData backs the Macros page.
type MacrosPageData struct {
	GeneratedAt string
	Analytics   store.MacroAnalytics
	// RecentMeals is the individual entries behind the averages. The
	// averages alone answer "how am I doing"; they can't answer "what was
	// in what I just ate", which is the question a per-meal breakdown is
	// for and the only place the macro data is visible per dish.
	RecentMeals []MealMacroRow
}

// MealMacroRow is one logged meal, formatted for display.
type MealMacroRow struct {
	Date      string
	DayLabel  string
	Person    string
	MealSlot  string
	Food      string
	Calories  float64
	Protein   float64
	Carbs     float64
	Fat       float64
	Fiber     float64
	HasMacros bool
}

// GetMacrosPageData assembles the macros page's data (trailing 7 days).
func GetMacrosPageData(db *sql.DB) (MacrosPageData, error) {
	since := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	analytics, err := store.GetMacroAnalytics(db, since)
	if err != nil {
		return MacrosPageData{}, err
	}
	entries, err := store.ListConsumptionEntries(db, store.ConsumptionListOpts{DateFrom: since})
	if err != nil {
		return MacrosPageData{}, err
	}

	rows := make([]MealMacroRow, 0, len(entries))
	for _, e := range entries {
		food := "-"
		switch {
		case e.FreeTextFood != nil && *e.FreeTextFood != "":
			food = *e.FreeTextFood
		case e.RecipeID != nil:
			food = *e.RecipeID
			if r, rerr := store.GetRecipe(db, *e.RecipeID); rerr == nil {
				food = r.Title
			}
		}
		slot := "-"
		if e.MealSlot != nil {
			slot = *e.MealSlot
		}
		day := e.ConsumedDate
		if t, terr := time.Parse("2006-01-02", e.ConsumedDate); terr == nil {
			day = t.Format("Mon Jan 2")
		}
		rows = append(rows, MealMacroRow{
			Date: e.ConsumedDate, DayLabel: day, Person: e.Person, MealSlot: slot, Food: food,
			Calories: derefFloat(e.Calories), Protein: derefFloat(e.Protein), Carbs: derefFloat(e.Carbs),
			Fat: derefFloat(e.Fat), Fiber: derefFloat(e.Fiber), HasMacros: e.Calories != nil,
		})
	}
	return MacrosPageData{
		GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm"),
		Analytics:   analytics,
		RecentMeals: rows,
	}, nil
}

func derefFloat(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// WastePageData backs the Waste page.
type WastePageData struct {
	GeneratedAt string
	Analytics   store.WasteAnalytics
}

// GetWastePageData assembles the waste page's data (trailing 30 days).
func GetWastePageData(db *sql.DB) (WastePageData, error) {
	since := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	analytics, err := store.GetWasteAnalytics(db, since)
	if err != nil {
		return WastePageData{}, err
	}
	return WastePageData{GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm"), Analytics: analytics}, nil
}

// VarietyPageData backs the Variety/Rotation page.
type VarietyPageData struct {
	GeneratedAt string
	Entries     []store.VarietyEntry
}

// GetVarietyPageData assembles the variety page's data.
func GetVarietyPageData(db *sql.DB) (VarietyPageData, error) {
	entries, err := store.GetVarietySummary(db, 0)
	if err != nil {
		return VarietyPageData{}, err
	}
	return VarietyPageData{GeneratedAt: time.Now().Format("Jan 2, 2006 3:04pm"), Entries: entries}, nil
}
