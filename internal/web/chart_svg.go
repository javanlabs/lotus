// chart_svg.go is task 4.6's rendering core: a pure, dependency-free
// server-side SVG line-chart builder. No chart library, no client
// component framework (task 4.1's own constraint) — just a <path>
// computed from repo.Point values and rendered inline in the response
// html/template already produces.
package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/repo"
)

// chartSVG is one rendered chart, flattened for a template.
type chartSVG struct {
	HasData  bool
	SVG      template.HTML
	Tone     string // pass|fail|none — the vocabulary lotus-badge/lotus-text already use
	MinLabel string
	MaxLabel string
	Latest   string
}

var chartToneColor = map[string]string{"pass": "#23808F", "fail": "#9E2B47", "none": "#6B7280"}

// buildChartSVG renders points as a gap-aware line: a point with
// HasValue=false breaks the path instead of pulling the line down to
// zero — task 4.6's single most load-bearing acceptance criterion (the
// spec's own words: drawing an empty bucket as zero is the most common
// and most expensive-to-misread mistake in metric charts, since a
// weekly-scanned SonarQube binding would otherwise look like it crashes
// six days out of seven). direction ("higher_better"/"lower_better"/
// "neutral") turns a first-value-to-last-value delta into pass/fail/none,
// the same tone vocabulary the rest of the app already colors by.
func buildChartSVG(points []repo.Point, direction, unit string, width, height float64) chartSVG {
	minV, maxV, haveAny := 0.0, 0.0, false
	for _, p := range points {
		if !p.HasValue {
			continue
		}
		if !haveAny || p.Value < minV {
			minV = p.Value
		}
		if !haveAny || p.Value > maxV {
			maxV = p.Value
		}
		haveAny = true
	}
	if !haveAny {
		return chartSVG{HasData: false}
	}
	valRange := maxV - minV
	if valRange == 0 {
		// A perfectly flat series: center the line rather than divide by
		// zero — still a real, meaningful chart (e.g. coverage steady at
		// 90% for the whole window), not an empty state.
		valRange = 1
	}

	from, to := points[0].At, points[len(points)-1].At
	span := to.Sub(from).Seconds()
	if span <= 0 {
		span = 1
	}
	// A small vertical margin keeps the line off the very top/bottom edge
	// so a flat-at-the-max or flat-at-the-min series doesn't clip its own
	// stroke width.
	const marginFrac = 0.08
	plotTop, plotHeight := height*marginFrac, height*(1-2*marginFrac)

	x := func(t time.Time) float64 { return t.Sub(from).Seconds() / span * width }
	y := func(v float64) float64 { return plotTop + plotHeight - (v-minV)/valRange*plotHeight }

	// Pass 1: first/last real value, purely to pick a tone — done before
	// building any markup so the path/dot-building pass below can write
	// the color inline instead of templating it in afterward.
	var firstVal, lastVal float64
	haveFirst := false
	for _, p := range points {
		if !p.HasValue {
			continue
		}
		if !haveFirst {
			firstVal = p.Value
			haveFirst = true
		}
		lastVal = p.Value
	}
	tone := "none"
	delta := lastVal - firstVal
	switch direction {
	case "higher_better":
		if delta > 0 {
			tone = "pass"
		} else if delta < 0 {
			tone = "fail"
		}
	case "lower_better":
		if delta < 0 {
			tone = "pass"
		} else if delta > 0 {
			tone = "fail"
		}
	}
	color := chartToneColor[tone]

	// Pass 2: build the path (one "M...L...L..." subpath per unbroken
	// run of real points, a fresh "M" every time a gap is crossed) and a
	// dot at every real point. The per-point dot matters specifically for
	// an isolated single-point run — a binding collected once a week
	// produces exactly that between two gaps, and a lone "M x,y" with no
	// following "L" draws nothing at all on its own; without a dot, that
	// whole week's real data point would be invisible, defeating the
	// entire purpose of rendering the gap instead of hiding it.
	var path, dots strings.Builder
	inRun := false
	for _, p := range points {
		if !p.HasValue {
			inRun = false
			continue
		}
		px, py := x(p.At), y(p.Value)
		cmd := "L"
		if !inRun {
			cmd = "M"
			inRun = true
		}
		fmt.Fprintf(&path, "%s%.2f,%.2f ", cmd, px, py)
		fmt.Fprintf(&dots, `<circle cx="%.2f" cy="%.2f" r="2.5" fill="%s" vector-effect="non-scaling-stroke"/>`, px, py, color)
	}

	// vector-effect="non-scaling-stroke": the <svg> is stretched to its
	// container via CSS (width/height:100%) rather than kept at its
	// viewBox's native aspect ratio, so a plain stroke-width would get
	// visually distorted (thicker on whichever axis is compressed more)
	// — this keeps the line and dots a uniform size regardless of that
	// stretch.
	svg := fmt.Sprintf(
		`<svg viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" style="width:100%%;height:100%%;display:block;overflow:visible">`+
			`<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>`+
			`%s`+
			`</svg>`,
		width, height, path.String(), color, dots.String(),
	)

	return chartSVG{
		HasData: true, SVG: template.HTML(svg), Tone: tone, //nolint:gosec // built entirely from numeric formatting above, no user input
		MinLabel: formatMetricValue(minV, unit), MaxLabel: formatMetricValue(maxV, unit),
		Latest: formatMetricValue(lastVal, unit),
	}
}

// formatMetricValue renders a raw metric_keys value using its catalog
// unit — the same "–"-for-absent convention every other detail page in
// this app already uses is the caller's job (this only ever sees real
// numbers), this just picks the right suffix/precision per unit.
func formatMetricValue(v float64, unit string) string {
	switch unit {
	case "%":
		return fmt.Sprintf("%.1f%%", v)
	case "ms":
		return fmt.Sprintf("%.0fms", v)
	case "minutes":
		return fmt.Sprintf("%.0fm", v)
	case "score":
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
