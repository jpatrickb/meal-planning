package dashboard

import (
	"fmt"
	"html"
	"html/template"
	"strings"
)

// Charts are inline SVG using CSS classes (not hardcoded fills), so the
// stylesheet's dark-mode media query re-colors them automatically - see
// style.css's --series-1/--series-2 etc. custom properties. Mark specs
// (≤24px bar thickness, 4px rounded data-end, 2px surface gap between bars,
// hairline gridlines, sparing direct labels) follow the project's dataviz
// skill guidance; the two-color patrick/thea palette (categorical slots 1-2)
// was run through validate_palette.js and passes every gate in both modes.

const barThickness = 20
const barGap = 2

// StackedCostBarChart renders weekly household cost as a stacked bar per
// week (patrick + thea), a "part-to-whole" chart per the form-selection
// guide. Only the household total is direct-labeled (2 series is within the
// "label sparingly" comfort zone; the legend carries per-person identity).
func StackedCostBarChart(weeks []WeeklyCost) template.HTML {
	if len(weeks) == 0 {
		return template.HTML(`<p class="empty">No cost data yet.</p>`)
	}
	const chartH = 200
	const barW = 32
	const gapW = 16
	const leftPad = 8
	const topPad = 24
	width := leftPad*2 + len(weeks)*(barW+gapW)

	maxTotal := 0.0
	for _, w := range weeks {
		if w.HouseholdCost > maxTotal {
			maxTotal = w.HouseholdCost
		}
	}
	if maxTotal == 0 {
		maxTotal = 1
	}
	scale := func(v float64) float64 { return v / maxTotal * (chartH - topPad) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" class="chart" role="img" aria-label="Weekly household grocery cost, split by person">`, width, chartH+30)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" class="axis-line"/>`, leftPad, chartH, width-leftPad, chartH)

	for i, w := range weeks {
		x := leftPad + i*(barW+gapW)
		theaH := scale(w.TheaCost)
		patrickH := scale(w.PatrickCost)
		theaY := float64(chartH) - theaH
		patrickY := theaY - patrickH
		if patrickY < 0 {
			patrickY = 0
		}

		if theaH > 0 {
			fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="%d" height="%.1f" rx="4" class="bar-thea"><title>%s: Thea $%.2f</title></rect>`,
				x, theaY, barW, theaH, html.EscapeString(w.Week), w.TheaCost)
		}
		if patrickH > 0 {
			gap := 0.0
			if theaH > 0 {
				gap = barGap
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="%d" height="%.1f" rx="4" class="bar-patrick"><title>%s: Patrick $%.2f</title></rect>`,
				x, patrickY-gap, barW, patrickH, html.EscapeString(w.Week), w.PatrickCost)
		}
		if w.HouseholdCost > 0 {
			labelY := patrickY - 6
			if labelY < 10 {
				labelY = 10
			}
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="bar-value">$%.0f</text>`, x+barW/2, labelY, w.HouseholdCost)
		}
		weekLabel := strings.TrimPrefix(w.Week, "20")
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="axis-label">%s</text>`, x+barW/2, chartH+16, html.EscapeString(weekLabel))
	}
	b.WriteString(`</svg>`)
	b.WriteString(`<div class="legend"><span class="legend-item"><span class="swatch swatch-patrick"></span>Patrick</span><span class="legend-item"><span class="swatch swatch-thea"></span>Thea</span></div>`)
	return template.HTML(b.String())
}

// HorizontalBarChart renders a single-series magnitude comparison
// (sequential blue), sorted by the caller, with a direct label on every bar -
// acceptable at this low cardinality (waste reasons: at most 5).
func HorizontalBarChart(labels []string, values []float64, valueFmt string) template.HTML {
	if len(labels) == 0 {
		return template.HTML(`<p class="empty">No data yet.</p>`)
	}
	const rowH = barThickness + 12
	const leftPad = 8
	const labelW = 120
	const chartW = 280
	height := len(labels)*rowH + 8

	maxV := 0.0
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	if maxV == 0 {
		maxV = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" class="chart" role="img" aria-label="Bar chart">`, leftPad+labelW+chartW+60, height)
	for i, label := range labels {
		y := i * rowH
		barLen := values[i] / maxV * chartW
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="axis-label" text-anchor="end">%s</text>`, leftPad+labelW-8, y+barThickness/2+4, html.EscapeString(label))
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.1f" height="%d" rx="4" class="bar-single"><title>%s: `+valueFmt+`</title></rect>`,
			leftPad+labelW, y, barLen, barThickness, html.EscapeString(label), values[i])
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="bar-value" text-anchor="start">`+valueFmt+`</text>`,
			float64(leftPad+labelW)+barLen+6, y+barThickness/2+4, values[i])
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// Meter renders a single-ratio-against-a-limit component: the fill is the
// accent hue, the unfilled track a lighter step of the same ramp, per the
// dataviz skill's meter spec. Overshoot (actual > target) fills the whole
// track and is still called out numerically in the label.
//
// With no target, the measured figure is still shown - just as a number with
// no track behind it. A value that was actually measured is worth reading
// whether or not someone has set a goal to compare it against, and showing
// only "No target set" hid every macro on the page from anyone who hadn't.
func Meter(label string, actual, target float64, unit string) template.HTML {
	if target <= 0 {
		return template.HTML(fmt.Sprintf(`<div class="meter">
		<div class="meter-label">%s</div>
		<div class="meter-figure">%.0f <span class="meter-unit">%s</span></div>
		<div class="meter-value">no target set</div>
	</div>`, html.EscapeString(label), actual, html.EscapeString(unit)))
	}
	ratio := actual / target
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	pct := ratio * 100
	return template.HTML(fmt.Sprintf(`<div class="meter">
		<div class="meter-label">%s</div>
		<svg viewBox="0 0 240 16" class="meter-track" role="img" aria-label="%s: %.0f of %.0f %s">
			<rect x="0" y="0" width="240" height="16" rx="8" class="meter-bg"/>
			<rect x="0" y="0" width="%.1f" height="16" rx="8" class="meter-fill"/>
		</svg>
		<div class="meter-value">%.0f / %.0f %s</div>
	</div>`, html.EscapeString(label), html.EscapeString(label), actual, target, unit, pct*2.4, actual, target, unit))
}
