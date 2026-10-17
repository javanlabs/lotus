package web

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/repo"
)

func pt(daysAgo int, value float64, hasValue bool) repo.Point {
	return repo.Point{At: time.Now().AddDate(0, 0, -daysAgo), Value: value, HasValue: hasValue}
}

// linePath is the d of a chart's line (the area under it is a second
// path, closed down to the axis).
func linePath(t *testing.T, svg string) string {
	t.Helper()
	i := strings.Index(svg, `<path data-line d="`)
	if i < 0 {
		t.Fatalf("no line path in:\n%s", svg)
	}
	rest := svg[i+len(`<path data-line d="`):]
	return rest[:strings.IndexByte(rest, '"')]
}

func TestBuildChartSVG_NoDataReturnsHasDataFalse(t *testing.T) {
	t.Parallel()
	if got := buildChartSVG(nil, "higher_better", "%", time.Time{}, time.Time{}, 100, 40); got.HasData {
		t.Error("HasData = true for nil points")
	}
	allEmpty := []repo.Point{pt(2, 0, false), pt(1, 0, false), pt(0, 0, false)}
	if got := buildChartSVG(allEmpty, "higher_better", "%", time.Time{}, time.Time{}, 100, 40); got.HasData {
		t.Error("HasData = true when every point has HasValue=false")
	}
}

// TestBuildChartSVG_GapBreaksThePathInsteadOfDroppingToZero is task 4.6's
// own most load-bearing acceptance criterion, quoted almost verbatim in
// the spec: a binding collected once a week must render as a gap, not as
// a line that plunges to zero between real points every day it wasn't
// collected. The path element is a single <path d="..."> — an empty
// bucket's HasValue=false must start a *new* "M" (moveto) subpath rather
// than an "L" (lineto) connecting through it.
func TestBuildChartSVG_GapBreaksThePathInsteadOfDroppingToZero(t *testing.T) {
	t.Parallel()
	points := []repo.Point{
		pt(6, 90, true),
		pt(5, 0, false),
		pt(4, 0, false),
		pt(3, 0, false),
		pt(2, 0, false),
		pt(1, 0, false),
		pt(0, 92, true),
	}
	got := buildChartSVG(points, "higher_better", "%", time.Time{}, time.Time{}, 500, 100)
	if !got.HasData {
		t.Fatal("HasData = false, want true (two real points exist)")
	}
	svg := linePath(t, string(got.SVG))
	moveCount := strings.Count(svg, "M")
	if moveCount != 2 {
		t.Fatalf("path has %d \"M\" (moveto) commands, want 2 — the 5-day gap between the two real points must start a new subpath, not one continuous line through the missing days:\n%s", moveCount, svg)
	}
}

func TestBuildChartSVG_ContinuousDataIsOnePathRun(t *testing.T) {
	t.Parallel()
	points := []repo.Point{pt(2, 10, true), pt(1, 20, true), pt(0, 30, true)}
	got := buildChartSVG(points, "higher_better", "count", time.Time{}, time.Time{}, 300, 80)
	if strings.Count(linePath(t, string(got.SVG)), "M") != 1 {
		t.Fatalf("path = %s, want exactly one \"M\" for an unbroken run", got.SVG)
	}
}

// TestBuildChartSVG_IsolatedSinglePointsAreStillVisible is the sharper
// form of the gap-not-zero requirement: a weekly-scanned binding doesn't
// just have *a* gap, every one of its real points is its own isolated
// run of length one, surrounded by gaps on both sides. A lone "M x,y"
// with no following "L" paints nothing at all — this was a real bug
// caught in manual browser QA (a sparse series rendered as a single dot
// for the latest value and nothing else, every earlier week's data
// invisible). Each real point must render its own <circle>, not just
// the series' last one.
func TestBuildChartSVG_IsolatedSinglePointsAreStillVisible(t *testing.T) {
	t.Parallel()
	points := []repo.Point{
		pt(21, 70, true), pt(20, 0, false), pt(19, 0, false), pt(18, 0, false),
		pt(14, 78, true), pt(13, 0, false), pt(12, 0, false), pt(11, 0, false),
		pt(7, 86, true), pt(6, 0, false), pt(5, 0, false), pt(4, 0, false),
		pt(0, 94, true),
	}
	got := buildChartSVG(points, "higher_better", "%", time.Time{}, time.Time{}, 500, 100)
	if got.MinLabel != "70.0%" || got.MaxLabel != "94.0%" {
		t.Fatalf("MinLabel/MaxLabel = %s/%s, want 70.0%%/94.0%%", got.MinLabel, got.MaxLabel)
	}
	circleCount := strings.Count(string(got.SVG), "<circle")
	if circleCount != 4 {
		t.Fatalf("SVG has %d <circle> markers, want 4 (one per real point, including the three isolated single-point runs):\n%s", circleCount, got.SVG)
	}
}

// TestBuildChartSVG_TrendToneFollowsDirectionNotAHardcodedList is task
// 4.6's other named acceptance criterion: coverage (higher_better) going
// up must read as improving, and a CVE count (lower_better) going up
// must read as worsening — both purely from the direction field repo.Point
// pairs with, never a per-metric special case.
func TestBuildChartSVG_TrendToneFollowsDirectionNotAHardcodedList(t *testing.T) {
	t.Parallel()
	rising := []repo.Point{pt(2, 70, true), pt(1, 80, true), pt(0, 90, true)}
	falling := []repo.Point{pt(2, 90, true), pt(1, 80, true), pt(0, 70, true)}

	if got := buildChartSVG(rising, "higher_better", "%", time.Time{}, time.Time{}, 200, 60); got.Tone != "pass" {
		t.Errorf("coverage (higher_better) rising: Tone = %q, want pass", got.Tone)
	}
	if got := buildChartSVG(falling, "higher_better", "%", time.Time{}, time.Time{}, 200, 60); got.Tone != "fail" {
		t.Errorf("coverage (higher_better) falling: Tone = %q, want fail", got.Tone)
	}
	if got := buildChartSVG(rising, "lower_better", "count", time.Time{}, time.Time{}, 200, 60); got.Tone != "fail" {
		t.Errorf("CVE count (lower_better) rising: Tone = %q, want fail", got.Tone)
	}
	if got := buildChartSVG(falling, "lower_better", "count", time.Time{}, time.Time{}, 200, 60); got.Tone != "pass" {
		t.Errorf("CVE count (lower_better) falling: Tone = %q, want pass", got.Tone)
	}
	if got := buildChartSVG(rising, "neutral", "count", time.Time{}, time.Time{}, 200, 60); got.Tone != "none" {
		t.Errorf("neutral direction: Tone = %q, want none regardless of trend", got.Tone)
	}
}

func TestBuildChartSVG_FlatSeriesDoesNotPanicOnZeroRange(t *testing.T) {
	t.Parallel()
	flat := []repo.Point{pt(2, 50, true), pt(1, 50, true), pt(0, 50, true)}
	got := buildChartSVG(flat, "higher_better", "%", time.Time{}, time.Time{}, 200, 60)
	if !got.HasData || got.Tone != "none" {
		t.Errorf("flat series: HasData=%v Tone=%q, want HasData=true Tone=none (no delta)", got.HasData, got.Tone)
	}
}

// readings is one reading every step from start, n of them, all at v.
func readings(start time.Time, step time.Duration, n int, v float64) []repo.Point {
	out := make([]repo.Point, n)
	for i := range out {
		out[i] = repo.Point{At: start.Add(time.Duration(i) * step), Value: v, HasValue: true}
	}
	return out
}

// The line is a smooth curve (cubic segments, no steps or straight
// lines), drawn through the readings where the value changed: a hundred
// identical readings hold one flat segment, and the curve turns over the
// one sync interval in which the value actually moved.
func TestBuildChartSVG_SmoothCurveThroughTheChanges(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	points := append(readings(start, 15*time.Minute, 100, 10), readings(start.Add(100*15*time.Minute), 15*time.Minute, 100, 20)...)
	got := buildChartSVG(points, "higher_better", "count", time.Time{}, time.Time{}, 400, 80)
	d := linePath(t, string(got.SVG))
	if strings.ContainsAny(d, "HVL") || !strings.Contains(d, "C") {
		t.Fatalf("want a curve of C segments only:\n%s", d)
	}
	// first, last-of-old, first-of-new, last: three segments.
	if n := strings.Count(d, "C"); n != 3 {
		t.Errorf("%d segments for one change in 200 readings, want 3:\n%s", n, d)
	}
	var hover []struct{ V, T string }
	if err := json.Unmarshal([]byte(got.Hover), &hover); err != nil || len(hover) != 4 || hover[1].V != "10" || hover[2].V != "20" {
		t.Errorf("hover points = %s (%v), want the four change points", got.Hover, err)
	}
}

// A monotone curve never draws a value that was not measured: every
// control point of a segment stays between that segment's two readings.
func TestMonotonePath_NeverOvershoots(t *testing.T) {
	t.Parallel()
	xs := []float64{0, 10, 11, 50, 51, 100}
	ys := []float64{50, 50, 10, 10, 90, 90}
	d := monotonePath(xs, ys)
	var segs [][6]float64
	for _, part := range strings.Split(d, " C")[1:] {
		var c [6]float64
		if _, err := fmt.Sscanf(part, "%f,%f %f,%f %f,%f", &c[0], &c[1], &c[2], &c[3], &c[4], &c[5]); err != nil {
			t.Fatalf("parse %q: %v", part, err)
		}
		segs = append(segs, c)
	}
	for i, c := range segs {
		lo, hi := math.Min(ys[i], ys[i+1]), math.Max(ys[i], ys[i+1])
		for _, y := range []float64{c[1], c[3]} {
			if y < lo-1e-6 || y > hi+1e-6 {
				t.Errorf("segment %d control y %.2f outside [%.0f, %.0f]: %s", i, y, lo, hi, d)
			}
		}
	}
}

// A small move on a large value does not fill the chart: coverage from
// 93.0% to 94.6% spans less than half the plot, not top to bottom.
func TestBuildChartSVG_SmallChangeIsNotACliff(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	points := append(readings(start, time.Hour, 5, 93.0), readings(start.Add(5*time.Hour), time.Hour, 5, 94.6)...)
	got := buildChartSVG(points, "higher_better", "%", time.Time{}, time.Time{}, 400, 100)
	var hover []struct{ Y float64 }
	if err := json.Unmarshal([]byte(got.Hover), &hover); err != nil || len(hover) < 2 {
		t.Fatalf("hover = %s (%v)", got.Hover, err)
	}
	if rise := hover[0].Y - hover[len(hover)-1].Y; rise <= 0 || rise > 50 {
		t.Errorf("a 1.6-point move rose %.1f%% of the plot, want under half", rise)
	}
	if got.MinLabel != "93.0%" || got.MaxLabel != "94.6%" {
		t.Errorf("labels = %s / %s, still the readings' own range", got.MinLabel, got.MaxLabel)
	}
}

// The shape that looked like a three-day climb: readings every 15 minutes,
// none for three and a half days, then readings again at a new value. The
// raw tier has no empty buckets to break on, so the stretch itself must
// break the line — two runs, and nothing drawn across the missing days.
// The window spans the axis: a reading at its middle sits mid-chart, and
// the latest value holds on to the window's end.
func TestBuildChartSVG_LongStretchWithoutReadingsBreaksTheLine(t *testing.T) {
	t.Parallel()
	to := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	mid := from.Add(7 * 24 * time.Hour / 2)
	points := append(readings(mid, 15*time.Minute, 8, 2066), readings(to.Add(-75*time.Minute), 15*time.Minute, 6, 2097)...)

	full := string(buildChartSVG(points, "neutral", "count", from, to, 700, 100).SVG)
	svg := linePath(t, full)
	if n := strings.Count(svg, "M"); n != 2 {
		t.Fatalf("%d runs, want 2 — the stretch with no readings must break the line:\n%s", n, svg)
	}
	if !strings.HasPrefix(svg, "M350.00,") {
		t.Errorf("first reading is not at the middle of the window:\n%s", svg)
	}
	if !strings.HasSuffix(strings.TrimSpace(svg[:strings.LastIndex(svg, ",")]), "700.00") {
		t.Errorf("the latest value does not hold to the window's end:\n%s", svg)
	}
	// One dot: the latest reading. Neither run is a lone reading.
	if n := strings.Count(full, "<circle"); n != 1 {
		t.Errorf("%d dots, want 1 (the latest reading)", n)
	}
}

// A series whose readings stopped long ago does not pretend its last
// value is still current.
func TestBuildChartSVG_StaleLastReadingDoesNotHoldToTheEnd(t *testing.T) {
	t.Parallel()
	to := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	points := readings(from.Add(time.Hour), time.Hour, 6, 5)
	svg := linePath(t, string(buildChartSVG(points, "neutral", "count", from, to, 700, 100).SVG))
	if strings.Contains(svg, "700.00,") {
		t.Fatalf("a stale series holds to the window's end:\n%s", svg)
	}
}

func TestGapThreshold(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := gapThreshold(readings(start, 15*time.Minute, 10, 1)); got != 45*time.Minute {
		t.Errorf("15-minute readings: %v, want 45m", got)
	}
	// Too few readings to have a usual spacing: never break on time.
	if got := gapThreshold(readings(start, time.Hour, 2, 1)); got != 0 {
		t.Errorf("two readings: %v, want 0", got)
	}
	// An empty bucket is not a spacing.
	pts := []repo.Point{{At: start, HasValue: true}, {At: start.Add(time.Hour)}, {At: start.Add(2 * time.Hour), HasValue: true}}
	if got := gapThreshold(pts); got != 0 {
		t.Errorf("readings split by an empty bucket: %v, want 0", got)
	}
}

func TestFormatMetricValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    float64
		unit string
		want string
	}{
		{92.345, "%", "92.3%"},
		{4200, "ms", "4.2s"},
		{15, "minutes", "15m"},
		{90, "minutes", "1h 30m"},
		{480, "minutes", "1d"},
		{59253, "minutes", "123d 3h"},
		{87, "score", "87"},
		{42, "count", "42"},
		{46264, "count", "46,264"},
		{270213, "count", "270,213"},
	}
	for _, c := range cases {
		if got := formatMetricValue(c.v, c.unit); got != c.want {
			t.Errorf("formatMetricValue(%v, %q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

func TestResolveChartRange(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		wantLabel  string
		wantAround time.Duration
	}{
		"7d":    {"7d", 7 * 24 * time.Hour},
		"30d":   {"30d", 30 * 24 * time.Hour},
		"bogus": {"7d", 7 * 24 * time.Hour},
		"":      {"7d", 7 * 24 * time.Hour},
	}
	for key, want := range cases {
		from, to, label := resolveChartRange(key)
		if label != want.wantLabel {
			t.Errorf("resolveChartRange(%q) label = %q, want %q", key, label, want.wantLabel)
		}
		width := to.Sub(from)
		// Allow a day of slack for AddDate's calendar-month/leap-year
		// wobble (1y is "minus one calendar year", not exactly 365*24h).
		diff := width - want.wantAround
		if diff < 0 {
			diff = -diff
		}
		if diff > 25*time.Hour {
			t.Errorf("resolveChartRange(%q) width = %v, want ~%v", key, width, want.wantAround)
		}
	}
}

func TestResolveChartRange_TiersMatchSeriesThresholds(t *testing.T) {
	t.Parallel()
	// task 4.6's acceptance criterion that the ranges "beneran muat
	// resolusi yang beda" depends entirely on each width landing on a
	// different repo.Series tier — this pins that invariant at the
	// boundary repo.Series itself defines (9d raw ceiling, 40d hourly
	// ceiling), so a change to either constant breaks a test instead of
	// silently producing two same-resolution charts.
	//
	// The daily tier no longer has a range that reaches it: 1y was
	// dropped, and 30d is now the widest window offered.
	from7, to7, _ := resolveChartRange("7d")
	from30, to30, _ := resolveChartRange("30d")

	if w := to7.Sub(from7); w > 9*24*time.Hour {
		t.Errorf("7d width = %v, want <= 9d (repo.Series' raw-tier ceiling) so it reads raw, not hourly", w)
	}
	if w := to30.Sub(from30); w <= 9*24*time.Hour || w > 40*24*time.Hour {
		t.Errorf("30d width = %v, want in (9d, 40d] (repo.Series' hourly tier)", w)
	}
	// And an unknown key still falls back to the narrowest window rather
	// than to whatever used to be widest.
	fromX, toX, labelX := resolveChartRange("1y")
	if labelX != "7d" || toX.Sub(fromX) > 9*24*time.Hour {
		t.Errorf("a retired range key resolved to %q/%v, want the 7d fallback", labelX, toX.Sub(fromX))
	}
}

func TestPrimaryChartKey(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"gitlab": "gitlab.pipeline_success_rate", "sonarqube": "sonar.coverage",
		"trivy": "trivy.total", "sentry": "sentry.score", "unknown": "",
	}
	for kind, want := range cases {
		if got := primaryChartKey(kind); got != want {
			t.Errorf("primaryChartKey(%q) = %q, want %q", kind, got, want)
		}
	}
}

// A change between two hourly syncs turns over at least easeFrac of the
// chart, not one hour of a week; the reading at the change stays put, and
// a held reading never moves behind the one before it.
func TestEaseChanges(t *testing.T) {
	t.Parallel()
	xs := []float64{0, 99, 100, 200}
	ys := []float64{10, 10, 20, 20}
	easeChanges(xs, ys, 20)
	if xs[1] != 80 || xs[2] != 100 {
		t.Errorf("xs = %v, want the held reading at 80 and the change still at 100", xs)
	}
	// Room for less than minW: it stops halfway to the reading before.
	xs = []float64{0, 95, 96, 100, 200}
	ys = []float64{10, 10, 20, 20, 20}
	easeChanges(xs, ys, 20)
	if xs[1] != 76 {
		t.Errorf("xs = %v, want the held reading at 76", xs)
	}
	xs2, ys2 := []float64{0, 90, 91}, []float64{10, 10, 20}
	easeChanges(xs2, ys2, 200)
	if xs2[1] != 45 {
		t.Errorf("xs = %v, want the held reading halfway back to the one before, 45", xs2)
	}
}

// dailyPoints is one point per day, oldest first, ending today; nil
// values are days before the first reading.
func dailyPoints(values ...*float64) []repo.Point {
	today := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	out := make([]repo.Point, len(values))
	for i, v := range values {
		out[i].At = today.AddDate(0, 0, i-len(values)+1)
		if v != nil {
			out[i].Value, out[i].HasValue = *v, true
		}
	}
	return out
}

func f(v float64) *float64 { return &v }

// The x axis is the window's days, evenly spaced: seven days are seven
// positions, each a dot and a hover point, the curve through all of them.
func TestBuildDailyChartSVG_OnePositionPerDay(t *testing.T) {
	t.Parallel()
	got := buildDailyChartSVG(dailyPoints(f(10), f(10), f(12), f(12), f(12), f(15), f(15)), "higher_better", "count", 560, 130)
	var hover []struct {
		X    float64
		V, T string
	}
	if err := json.Unmarshal([]byte(got.Hover), &hover); err != nil || len(hover) != 7 {
		t.Fatalf("hover = %s (%v), want 7 days", got.Hover, err)
	}
	step := hover[1].X - hover[0].X
	for i := 1; i < len(hover); i++ {
		if d := hover[i].X - hover[i-1].X; math.Abs(d-step) > 0.15 {
			t.Errorf("day %d is %.1f%% from the one before, want an even %.1f%%", i, d, step)
		}
	}
	if hover[6].T != "18 Oct" || hover[0].T != "12 Oct" || hover[6].V != "15" {
		t.Errorf("hover ends = %+v … %+v", hover[0], hover[6])
	}
	svg := string(got.SVG)
	if n := strings.Count(svg, " h0\""); n != 7 {
		t.Errorf("%d day dots, want 7", n)
	}
	if d := linePath(t, svg); strings.Count(d, "M") != 1 || strings.Count(d, "C") != 6 {
		t.Errorf("want one unbroken curve of six segments:\n%s", d)
	}
	if got.Tone != "pass" || got.Latest != "15" {
		t.Errorf("tone %q latest %q", got.Tone, got.Latest)
	}
}

// Days before the first reading are left empty and the curve starts at
// the first that has one, each of those ten days a dot.
func TestBuildDailyChartSVG_StartsAtTheFirstReading(t *testing.T) {
	t.Parallel()
	values := make([]*float64, 30)
	for i := 20; i < 30; i++ {
		values[i] = f(90)
	}
	got := buildDailyChartSVG(dailyPoints(values...), "higher_better", "%", 560, 130)
	var hover []struct{ X float64 }
	if err := json.Unmarshal([]byte(got.Hover), &hover); err != nil || len(hover) != 10 {
		t.Fatalf("hover = %s, want the ten days with a value", got.Hover)
	}
	if hover[0].X < 60 {
		t.Errorf("the curve starts at %.1f%%, want two thirds of the way in", hover[0].X)
	}
	if n := strings.Count(string(got.SVG), " h0\""); n != 10 {
		t.Errorf("%d dots for ten days, want one per day", n)
	}
	if got := buildDailyChartSVG(dailyPoints(make([]*float64, 7)...), "neutral", "count", 560, 130); got.HasData {
		t.Error("a window with no reading at all has data")
	}
}
