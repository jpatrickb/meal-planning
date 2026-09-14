package dashboard

import (
	"bytes"
	"database/sql"
	"embed"
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/*.html.tmpl templates/style.css
var templatesFS embed.FS

type navEntry struct {
	Title, File string
}

var navEntries = []navEntry{
	{"Home", "index.html"},
	{"Meal Plan", "plan.html"},
	{"Shopping List", "shopping.html"},
	{"Pantry", "pantry.html"},
	{"Cost & Trends", "cost.html"},
	{"Macros vs Targets", "macros.html"},
	{"Waste", "waste.html"},
	{"Variety", "variety.html"},
}

type shellData struct {
	Title       string
	GeneratedAt string
	NavHTML     template.HTML
	Content     template.HTML
}

// Render generates the full static site (8 HTML pages + style.css) into outDir.
func Render(db *sql.DB, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	cssBytes, err := templatesFS.ReadFile("templates/style.css")
	if err != nil {
		return fmt.Errorf("reading style.css: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), cssBytes, 0o644); err != nil {
		return fmt.Errorf("writing style.css: %w", err)
	}

	homeData, err := GetHomeData(db)
	if err != nil {
		return fmt.Errorf("loading home data: %w", err)
	}
	if err := renderPage(outDir, "home.html.tmpl", "index.html", "Home", homeData.GeneratedAt, homeData); err != nil {
		return err
	}

	planData, err := GetPlanPageData(db)
	if err != nil {
		return fmt.Errorf("loading plan data: %w", err)
	}
	if err := renderPage(outDir, "plan.html.tmpl", "plan.html", "Meal Plan", planData.GeneratedAt, planData); err != nil {
		return err
	}

	shoppingData, err := GetShoppingPageData(db)
	if err != nil {
		return fmt.Errorf("loading shopping list data: %w", err)
	}
	if err := renderPage(outDir, "shopping.html.tmpl", "shopping.html", "Shopping List", shoppingData.GeneratedAt, shoppingData); err != nil {
		return err
	}

	costData, err := GetCostPageData(db)
	if err != nil {
		return fmt.Errorf("loading cost data: %w", err)
	}
	costView := struct {
		CostPageData
		Chart template.HTML
	}{costData, StackedCostBarChart(costData.Weekly)}
	if err := renderPage(outDir, "cost.html.tmpl", "cost.html", "Cost & Trends", costData.GeneratedAt, costView); err != nil {
		return err
	}

	pantryData, err := GetPantryPageData(db)
	if err != nil {
		return fmt.Errorf("loading pantry data: %w", err)
	}
	if err := renderPage(outDir, "pantry.html.tmpl", "pantry.html", "Pantry", pantryData.GeneratedAt, pantryData); err != nil {
		return err
	}

	macrosData, err := GetMacrosPageData(db)
	if err != nil {
		return fmt.Errorf("loading macros data: %w", err)
	}
	if err := renderPage(outDir, "macros.html.tmpl", "macros.html", "Macros vs Targets", macrosData.GeneratedAt, buildMacrosView(macrosData)); err != nil {
		return err
	}

	wasteData, err := GetWastePageData(db)
	if err != nil {
		return fmt.Errorf("loading waste data: %w", err)
	}
	var labels []string
	var values []float64
	for _, rw := range wasteData.Analytics.ByReason {
		labels = append(labels, rw.Reason)
		values = append(values, rw.TotalCost)
	}
	wasteView := struct {
		WastePageData
		Chart template.HTML
	}{wasteData, HorizontalBarChart(labels, values, "$%.2f")}
	if err := renderPage(outDir, "waste.html.tmpl", "waste.html", "Waste", wasteData.GeneratedAt, wasteView); err != nil {
		return err
	}

	varietyData, err := GetVarietyPageData(db)
	if err != nil {
		return fmt.Errorf("loading variety data: %w", err)
	}
	if err := renderPage(outDir, "variety.html.tmpl", "variety.html", "Variety", varietyData.GeneratedAt, varietyData); err != nil {
		return err
	}

	return nil
}

type personMeterBlock struct {
	Person     string
	DaysLogged int
	MetersHTML template.HTML
}

func buildMacrosView(data MacrosPageData) any {
	var blocks []personMeterBlock
	for _, pm := range data.Analytics.ByPerson {
		var b strings.Builder
		b.WriteString(string(Meter("Calories", pm.AvgCalories, derefOr(pm.Target.Kcal), "kcal")))
		b.WriteString(string(Meter("Protein", pm.AvgProtein, derefOr(pm.Target.Protein), "g")))
		b.WriteString(string(Meter("Carbs", pm.AvgCarbs, derefOr(pm.Target.Carbs), "g")))
		b.WriteString(string(Meter("Fat", pm.AvgFat, derefOr(pm.Target.Fat), "g")))
		blocks = append(blocks, personMeterBlock{Person: pm.Person, DaysLogged: pm.DaysLogged, MetersHTML: template.HTML(b.String())})
	}
	return struct {
		MacrosPageData
		PersonMeters []personMeterBlock
	}{data, blocks}
}

func derefOr(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func renderPage(outDir, tmplFile, outFile, title, generatedAt string, data any) error {
	contentTmpl, err := template.ParseFS(templatesFS, "templates/"+tmplFile)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", tmplFile, err)
	}
	var contentBuf bytes.Buffer
	if err := contentTmpl.ExecuteTemplate(&contentBuf, "content", data); err != nil {
		return fmt.Errorf("executing %s: %w", tmplFile, err)
	}

	baseTmpl, err := template.ParseFS(templatesFS, "templates/base.html.tmpl")
	if err != nil {
		return fmt.Errorf("parsing base template: %w", err)
	}
	shell := shellData{
		Title: title, GeneratedAt: generatedAt,
		NavHTML: buildNav(outFile), Content: template.HTML(contentBuf.String()),
	}
	var pageBuf bytes.Buffer
	if err := baseTmpl.ExecuteTemplate(&pageBuf, "base", shell); err != nil {
		return fmt.Errorf("executing base template for %s: %w", outFile, err)
	}

	if err := os.WriteFile(filepath.Join(outDir, outFile), pageBuf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outFile, err)
	}
	return nil
}

func buildNav(activeFile string) template.HTML {
	var b strings.Builder
	for _, e := range navEntries {
		class := ""
		if e.File == activeFile {
			class = ` class="active"`
		}
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, html.EscapeString(e.File), class, html.EscapeString(e.Title))
	}
	return template.HTML(b.String())
}
