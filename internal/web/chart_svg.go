// chart_svg.go is task 4.6's rendering core: a pure, dependency-free
// server-side SVG line-chart builder. No chart library, no client
// component framework (task 4.1's own constraint) — just a <path>
// computed from repo.Point values and rendered inline in the response
// html/template already produces.
package web

import (
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
	valRange := maxV - minV
	if valRange == 0 {
		// A perfectly flat series: center the line rather than divide by
		// zero — still a real, meaningful chart (e.g. coverage steady at
		// 90% for the whole window), not an empty state.
		valRange = 1
	}

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
	const marginFrac = 0.08
	plotTop, plotHeight := height*marginFrac, height*(1-2*marginFrac)

	// Clamped: a rollup bucket starts on the hour, so the first can sit
	// slightly before the window.
	x := func(t time.Time) float64 {
		return math.Min(width, math.Max(0, t.Sub(from).Seconds()/span*width))
	}
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

	// Pass 2: split the readings into runs — broken by an empty bucket
	// or by a stretch longer than gapThreshold — and draw each as one
	// "M x,y H.. V.." step subpath.
	//
	// Dots mark what a line cannot: a run of a single reading (a binding
	// collected once a week produces exactly that between two gaps, and a
	// lone "M x,y" with no following segment draws nothing at all —
	// without a dot, that week's real reading would be invisible) and the
	// latest reading, the value the panel's number repeats. A dot on every
	// reading of a dense series only buried the line under them.
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

	dot := func(b *strings.Builder, p repo.Point) {
		fmt.Fprintf(b, `<circle cx="%.2f" cy="%.2f" r="2.5" fill="%s" vector-effect="non-scaling-stroke"/>`, x(p.At), y(p.Value), color)
	}
	var path, dots strings.Builder
	for i, r := range runs {
		fmt.Fprintf(&path, "M%.2f,%.2f ", x(r[0].At), y(r[0].Value))
		for _, p := range r[1:] {
			fmt.Fprintf(&path, "H%.2f V%.2f ", x(p.At), y(p.Value))
		}
		last := r[len(r)-1]
		latest := i == len(runs)-1
		// The current value holds to the window's end — but only while it
		// is still current: a series whose last reading is old stops
		// where its readings do.
		if latest && maxGap > 0 && to.Sub(last.At) <= maxGap {
			fmt.Fprintf(&path, "H%.2f ", x(to))
		}
		if len(r) == 1 || latest {
			dot(&dots, last)
		}
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
