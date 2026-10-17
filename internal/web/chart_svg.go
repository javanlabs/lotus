// chart_svg.go is task 4.6's rendering core: a pure, dependency-free
// server-side SVG line-chart builder. No chart library, no client
// component framework (task 4.1's own constraint) — just a <path>
// computed from repo.Point values and rendered inline in the response
// html/template already produces.
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
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
	// Hover is the chart's readings as JSON for its hover readout
	// (lotusChart in web/static/js/lotus.js): each point's position as a
	// percentage of the plot, its formatted value and its time.
	Hover string

	// SparkPoints and SparkColor feed the client-side sparkline
	// (web/static/vendor/sparklines.js), which takes a flat list of
	// numbers. It has no way to express a gap, so ONLY points with a
	// value are listed: an empty bucket is omitted rather than drawn as
	// zero. The series therefore compresses — a binding collected weekly
	// renders as a dense line rather than a sparse one — which is the
	// trade the library imposes. SVG above stays gap-aware and is still
	// what the metric detail pages draw.
	SparkPoints string
	SparkColor  string
}

// sparkMaxBars caps how many bars a 90px sparkline is asked to draw. See
// the downsampling in buildChartSVG for why this is a hard requirement of
// sparklines.js and not just a taste call.
const sparkMaxBars = 24

var chartToneColor = map[string]string{"pass": "#23808F", "fail": "#9E2B47", "none": "#6B7280"}

// gapFactor is how many times the series' usual spacing two readings may
// be apart and still be drawn as one run; further apart, the line breaks.
const gapFactor = 3

// gapThreshold is the longest stretch between two consecutive readings
// that is still "the same run": gapFactor times the median spacing of
// the series' own readings. Measured from the data, not configured: a
// binding synced every 15 minutes breaks after 45, a daily rollup after
// three days. Zero (never break on time) when there are too few readings
// to have a usual spacing.
func gapThreshold(points []repo.Point) time.Duration {
	var spacings []time.Duration
	var prev *repo.Point
	for i := range points {
		p := &points[i]
		if !p.HasValue {
			prev = nil
			continue
		}
		if prev != nil {
			spacings = append(spacings, p.At.Sub(prev.At))
		}
		prev = p
	}
	if len(spacings) < 2 {
		return 0
	}
	sort.Slice(spacings, func(i, j int) bool { return spacings[i] < spacings[j] })
	return gapFactor * spacings[len(spacings)/2]
}

// buildChartSVG renders points as a gap-aware step line over [from, to],
// the window the caller asked Series for (zero values fall back to the
// first and last point).
//
// Gap-aware: a point with HasValue=false breaks the path instead of
// pulling the line down to zero — task 4.6's single most load-bearing
// acceptance criterion (the spec's own words: drawing an empty bucket as
// zero is the most common and most expensive-to-misread mistake in
// metric charts, since a weekly-scanned SonarQube binding would otherwise
// look like it crashes six days out of seven). The raw tier marks no
// empty buckets — it returns only the readings there are — so a stretch
// with no readings longer than gapThreshold breaks the path too, instead
// of being bridged by a diagonal that reads as a change spread over days
// that were never measured.
//
// Step: a reading's value holds until the next one, then the line moves
// straight to the new value. The integrations report values that change
// at a scan or a pipeline, not continuously; a slope between two readings
// would show a gradual change that did not happen. The latest run holds
// on to the end of the window when its last reading is recent enough to
// still be the current value.
//
// The window, not the readings, spans the x axis, so a reading sits at
// its time and a stretch with none reads as empty.
//
// direction ("higher_better"/"lower_better"/"neutral") turns a
// first-value-to-last-value delta into pass/fail/none, the same tone
// vocabulary the rest of the app already colors by.
func buildChartSVG(points []repo.Point, direction, unit string, from, to time.Time, width, height float64) chartSVG {
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
	// The y axis spans the readings, but never less than minSpanFrac of
	// their size: zoomed all the way in, coverage moving from 93.0% to
	// 94.6% filled the chart top to bottom and read as a collapse. A
	// perfectly flat series centres its line (still a real chart —
	// coverage steady at 90% all week — not an empty state).
	const minSpanFrac = 0.05
	lo, hi := minV, maxV
	if span := math.Max(math.Abs(lo+hi)/2*minSpanFrac, 1e-9); hi-lo < span {
		mid := (lo + hi) / 2
		lo, hi = mid-span/2, mid+span/2
		if minV >= 0 && lo < 0 { // a count or a percentage never dips below zero
			lo, hi = 0, span
		}
	}
	valRange := hi - lo

	if from.IsZero() || to.IsZero() {
		from, to = points[0].At, points[len(points)-1].At
	}
	span := to.Sub(from).Seconds()
	if span <= 0 {
		span = 1
	}
	// A small vertical margin keeps the line off the very top/bottom edge
	// so a flat-at-the-max or flat-at-the-min series doesn't clip its own
	// stroke width.
	const marginFrac = 0.1
	plotTop, plotHeight := height*marginFrac, height*(1-2*marginFrac)

	// Clamped: a rollup bucket starts on the hour, so the first can sit
	// slightly before the window.
	x := func(t time.Time) float64 {
		return math.Min(width, math.Max(0, t.Sub(from).Seconds()/span*width))
	}
	y := func(v float64) float64 { return plotTop + plotHeight - (v-lo)/valRange*plotHeight }

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

	// Pass 2: split the readings into runs — broken by an empty bucket
	// or by a stretch longer than gapThreshold — so a stretch with no
	// readings is drawn as nothing, never bridged.
	maxGap := gapThreshold(points)
	var runs [][]repo.Point
	var run []repo.Point
	for _, p := range points {
		if !p.HasValue {
			if len(run) > 0 {
				runs = append(runs, run)
				run = nil
			}
			continue
		}
		if len(run) > 0 && maxGap > 0 && p.At.Sub(run[len(run)-1].At) > maxGap {
			runs = append(runs, run)
			run = nil
		}
		run = append(run, p)
	}
	if len(run) > 0 {
		runs = append(runs, run)
	}

	// Pass 3: draw each run as a smooth curve through the readings where
	// the value changed. A collect every 15 minutes repeats the same value
	// a hundred times a day; only the reading before a change and the one
	// after it carry information, so the curve holds flat until then and
	// turns over that one sync interval — the change shows when it
	// happened, without a vertical cliff. The curve is monotone (it never
	// overshoots past either neighbouring reading), so a smooth line never
	// draws a value that was not measured.
	//
	// Dots mark what a line cannot: a run of a single reading (a binding
	// collected weekly has exactly that between two gaps) and the latest
	// reading, the value the panel's number repeats.
	type hoverPoint struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		V string  `json:"v"`
		T string  `json:"t"`
	}
	var hover []hoverPoint
	dot := func(b *strings.Builder, p repo.Point) {
		fmt.Fprintf(b, `<circle cx="%.2f" cy="%.2f" r="2.5" fill="%s" vector-effect="non-scaling-stroke"/>`, x(p.At), y(p.Value), color)
	}
	var line, area, dots strings.Builder
	for i, r := range runs {
		kept := changePoints(r)
		last := r[len(r)-1]
		latest := i == len(runs)-1
		// The current value holds to the window's end — but only while it
		// is still current: a series whose last reading is old stops
		// where its readings do.
		if latest && maxGap > 0 && to.Sub(last.At) <= maxGap {
			kept = append(kept, repo.Point{At: to, Value: last.Value, HasValue: true})
		}
		xs, ys := make([]float64, 0, len(kept)), make([]float64, 0, len(kept))
		at := make([]repo.Point, 0, len(kept)) // the reading behind each (xs, ys)
		for _, p := range kept {
			px, py := x(p.At), y(p.Value)
			if n := len(xs); n > 0 && px <= xs[n-1] {
				ys[n-1], at[n-1] = py, p // two readings on one x: the later one stands
				continue
			}
			xs, ys, at = append(xs, px), append(ys, py), append(at, p)
		}
		easeChanges(xs, ys, width*easeFrac)
		// Hover reads the curve as drawn — after easing — so the ring sits
		// on the line; the hold-to-end point is not a reading of its own.
		for j := range xs {
			if at[j].At.Equal(to) && j == len(xs)-1 && j > 0 && !last.At.Equal(to) {
				continue
			}
			hover = append(hover, hoverPoint{
				X: math.Round(xs[j]/width*1000) / 10, Y: math.Round(ys[j]/height*1000) / 10,
				V: formatMetricValue(at[j].Value, unit), T: at[j].At.Local().Format("02 Jan 15:04"),
			})
		}
		if len(xs) > 1 {
			d := monotonePath(xs, ys)
			fmt.Fprintf(&line, "%s ", d)
			fmt.Fprintf(&area, "%s L%.2f,%.2f L%.2f,%.2f Z ", d, xs[len(xs)-1], height, xs[0], height)
		} else {
			// A lone reading paints nothing as a path; its dot below is
			// what shows it. The moveto still marks it as its own run.
			fmt.Fprintf(&line, "M%.2f,%.2f ", xs[0], ys[0])
		}
		if len(xs) == 1 || len(r) == 1 || latest {
			dot(&dots, last)
		}
	}
	hoverJSON, _ := json.Marshal(hover)

	// vector-effect="non-scaling-stroke": the <svg> is stretched to its
	// container via CSS (width/height:100%) rather than kept at its
	// viewBox's native aspect ratio, so a plain stroke-width would get
	// visually distorted (thicker on whichever axis is compressed more)
	// — this keeps the line and dots a uniform size regardless of that
	// stretch. The area under the line is a faint wash of the same
	// colour, so the shape reads at a glance without competing with it.
	svg := fmt.Sprintf(
		`<svg viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" style="width:100%%;height:100%%;display:block;overflow:visible">`+
			`<path data-area d="%s" fill="%s" fill-opacity=".08" stroke="none"/>`+
			`<path data-line d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>`+
			`%s`+
			`</svg>`,
		width, height, strings.TrimSpace(area.String()), color, strings.TrimSpace(line.String()), color, dots.String(),
	)

	// Rescaled into a 20-100 window rather than passed raw. sparklines.js
	// scales from ZERO, not from the series minimum, so a metric that
	// lives between 70 and 90 draws as a row of near-identical full-height
	// bars and the shape — the whole point of a sparkline — disappears.
	//
	// The floor is 20, not 0: these are bars, and mapping the minimum to
	// zero would render the lowest bucket as a bar of no height at all,
	// which reads as missing data rather than as the low point. The
	// absolute numbers are not on the chart anyway; the title carries the
	// latest value.
	// Downsampled to at most sparkMaxBars buckets before anything else.
	// Two reasons, one cosmetic and one fatal: 90px cannot show a raw 7d
	// series (a few hundred readings) as bars, and sparklines.js sizes a
	// bar as (gap/n + width/n - gap), which goes NEGATIVE once n exceeds
	// width+gap — the browser then rejects every <rect> with "attribute
	// width: A negative value is not valid" and the chart is blank.
	real := make([]repo.Point, 0, len(points))
	for _, p := range points {
		if p.HasValue {
			real = append(real, p)
		}
	}
	sampled := real
	if len(real) > sparkMaxBars {
		sampled = make([]repo.Point, 0, sparkMaxBars)
		for i := 0; i < sparkMaxBars; i++ {
			// Mean of the bucket, not a sample of it: a spike that lands
			// between two sampled indices should still move the bar.
			lo := i * len(real) / sparkMaxBars
			hi := (i + 1) * len(real) / sparkMaxBars
			if hi <= lo {
				hi = lo + 1
			}
			sum := 0.0
			for _, p := range real[lo:hi] {
				sum += p.Value
			}
			sampled = append(sampled, repo.Point{HasValue: true, Value: sum / float64(hi-lo), At: real[lo].At})
		}
	}

	var spark []string
	for _, p := range sampled {
		if !p.HasValue {
			continue
		}
		// A flat series has no window to rescale into, so it sits at
		// mid-height instead of collapsing to the floor.
		n := 60.0
		if maxV > minV {
			n = 20 + (p.Value-minV)/(maxV-minV)*80
		}
		spark = append(spark, strconv.FormatFloat(n, 'f', 2, 64))
	}

	return chartSVG{
		HasData: true, SVG: template.HTML(svg), Tone: tone, //nolint:gosec // built entirely from numeric formatting above, no user input
		MinLabel: formatMetricValue(minV, unit), MaxLabel: formatMetricValue(maxV, unit),
		Latest:      formatMetricValue(lastVal, unit),
		Hover:       string(hoverJSON),
		SparkPoints: strings.Join(spark, ","),
		SparkColor:  color,
	}
}

// formatMetricValue renders a raw metric_keys value using its catalog
// unit — the same "–"-for-absent convention every other detail page in
// this app already uses is the caller's job (this only ever sees real
// numbers), this just picks the right suffix/precision per unit. Counts
// get thousands separators and durations read as durations, the way the
// sonar detail page writes the same numbers: "46,264", not "46264";
// "123d 3h", not "59253m".
func formatMetricValue(v float64, unit string) string {
	switch unit {
	case "%":
		return fmt.Sprintf("%.1f%%", v)
	case "ms":
		return humanDuration(int64(math.Round(v)))
	case "minutes":
		return humanMinutes(int64(math.Round(v)))
	default:
		return thousands(int64(math.Round(v)))
	}
}

// humanMinutes writes an effort in minutes (SonarQube's technical debt)
// the way SonarQube itself does: a day is 8 working hours, and only the
// two largest units are kept.
func humanMinutes(m int64) string {
	const hour, day = 60, 8 * 60
	switch {
	case m < 0:
		return "-" + humanMinutes(-m)
	case m < hour:
		return fmt.Sprintf("%dm", m)
	case m < day:
		if m%hour == 0 {
			return fmt.Sprintf("%dh", m/hour)
		}
		return fmt.Sprintf("%dh %dm", m/hour, m%hour)
	default:
		d, h := m/day, (m%day)/hour
		if h == 0 {
			return thousands(d) + "d"
		}
		return fmt.Sprintf("%sd %dh", thousands(d), h)
	}
}

// changePoints keeps the readings of a run that say something: the
// first, the last, and on each side of every change the reading before it
// (the old value's last) and the one at it (the new value's first). A run
// that never changes keeps two readings, not a hundred.
func changePoints(r []repo.Point) []repo.Point {
	out := []repo.Point{r[0]}
	for i := 1; i < len(r); i++ {
		if r[i].Value == r[i-1].Value {
			continue
		}
		if !out[len(out)-1].At.Equal(r[i-1].At) {
			out = append(out, r[i-1])
		}
		out = append(out, r[i])
	}
	if last := r[len(r)-1]; !out[len(out)-1].At.Equal(last.At) {
		out = append(out, last)
	}
	return out
}

// monotonePath is an SVG path through (xs, ys) as a monotone cubic curve
// (Fritsch–Carlson, the shape d3's curveMonotoneX draws): smooth, but it
// never rises above or dips below the two readings either side of a
// segment, so between a flat stretch and a change it turns without the
// overshoot an ordinary spline would invent. xs must strictly increase.
func monotonePath(xs, ys []float64) string {
	n := len(xs)
	sign := func(v float64) float64 {
		switch {
		case v > 0:
			return 1
		case v < 0:
			return -1
		}
		return 0
	}
	slope := make([]float64, n-1)
	for i := range slope {
		slope[i] = (ys[i+1] - ys[i]) / (xs[i+1] - xs[i])
	}
	tangent := make([]float64, n)
	for i := 1; i < n-1; i++ {
		h0, h1 := xs[i]-xs[i-1], xs[i+1]-xs[i]
		s0, s1 := slope[i-1], slope[i]
		p := (s0*h1 + s1*h0) / (h0 + h1)
		tangent[i] = (sign(s0) + sign(s1)) * math.Min(math.Min(math.Abs(s0), math.Abs(s1)), 0.5*math.Abs(p))
	}
	// Ends: d3's slope2 — the one-sided estimate, bent toward the inner
	// tangent.
	tangent[0] = slope[0]
	tangent[n-1] = slope[n-2]
	if n > 2 {
		tangent[0] = (3*slope[0] - tangent[1]) / 2
		tangent[n-1] = (3*slope[n-2] - tangent[n-2]) / 2
		if sign(tangent[0]) != sign(slope[0]) {
			tangent[0] = 0
		}
		if sign(tangent[n-1]) != sign(slope[n-2]) {
			tangent[n-1] = 0
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M%.2f,%.2f", xs[0], ys[0])
	for i := 0; i < n-1; i++ {
		dx := (xs[i+1] - xs[i]) / 3
		fmt.Fprintf(&b, " C%.2f,%.2f %.2f,%.2f %.2f,%.2f",
			xs[i]+dx, ys[i]+tangent[i]*dx, xs[i+1]-dx, ys[i+1]-tangent[i+1]*dx, xs[i+1], ys[i+1])
	}
	return b.String()
}

// easeFrac is the narrowest a change's turn is drawn, as a share of the
// chart's width. A value that moved between two hourly syncs turns over
// one hour — on a week-wide chart, a vertical cliff. Widening only the
// turn keeps the change where it happened and lets the curve read as a
// curve.
const easeFrac = 0.04

// easeChanges moves the last reading before each change back, to at
// least minW before the change, but never past the reading before it:
// the old value then curves into the new one over that stretch instead of
// in a single sync interval. Only a held reading moves (one whose value
// matches the reading before it), so no measured change shifts in time.
func easeChanges(xs, ys []float64, minW float64) {
	for i := 1; i < len(xs)-1; i++ {
		if ys[i] == ys[i+1] || ys[i] != ys[i-1] {
			continue
		}
		want := xs[i+1] - minW
		if want < xs[i] {
			xs[i] = math.Max(want, xs[i-1]+(xs[i]-xs[i-1])/2)
		}
	}
}
