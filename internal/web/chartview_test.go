package web

import (
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/repo"
)

func pt(daysAgo int, value float64, hasValue bool) repo.Point {
	return repo.Point{At: time.Now().AddDate(0, 0, -daysAgo), Value: value, HasValue: hasValue}
}

func TestBuildChartSVG_NoDataReturnsHasDataFalse(t *testing.T) {
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
	svg := string(got.SVG)
	moveCount := strings.Count(svg, "M")
	if moveCount != 2 {
		t.Fatalf("path has %d \"M\" (moveto) commands, want 2 — the 5-day gap between the two real points must start a new subpath, not one continuous line through the missing days:\n%s", moveCount, svg)
	}
}

func TestBuildChartSVG_ContinuousDataIsOnePathRun(t *testing.T) {
	points := []repo.Point{pt(2, 10, true), pt(1, 20, true), pt(0, 30, true)}
	got := buildChartSVG(points, "higher_better", "count", time.Time{}, time.Time{}, 300, 80)
	if strings.Count(string(got.SVG), "M") != 1 {
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

// A value holds until the next reading: the path steps (H then V) between
// readings instead of sloping across them.
func TestBuildChartSVG_StepsBetweenReadings(t *testing.T) {
	points := []repo.Point{pt(2, 10, true), pt(1, 20, true), pt(0, 30, true)}
	svg := string(buildChartSVG(points, "higher_better", "count", time.Time{}, time.Time{}, 300, 80).SVG)
	if strings.Contains(svg, "L") || strings.Count(svg, "H") < 2 || strings.Count(svg, "V") != 2 {
		t.Fatalf("want a step path (M, then H/V per reading, no L):\n%s", svg)
	}
}

// The shape that looked like a three-day climb: readings every 15 minutes,
// none for three and a half days, then readings again at a new value. The
// raw tier has no empty buckets to break on, so the stretch itself must
// break the line — two runs, and nothing drawn across the missing days.
// The window spans the axis: a reading at its middle sits mid-chart, and
// the latest value holds on to the window's end.
func TestBuildChartSVG_LongStretchWithoutReadingsBreaksTheLine(t *testing.T) {
	to := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	mid := from.Add(7 * 24 * time.Hour / 2)
	points := append(readings(mid, 15*time.Minute, 8, 2066), readings(to.Add(-75*time.Minute), 15*time.Minute, 6, 2097)...)

	svg := string(buildChartSVG(points, "neutral", "count", from, to, 700, 100).SVG)
	if n := strings.Count(svg, "M"); n != 2 {
		t.Fatalf("%d runs, want 2 — the stretch with no readings must break the line:\n%s", n, svg)
	}
	if !strings.HasPrefix(svg[strings.Index(svg, `d="`)+3:], "M350.00,") {
		t.Errorf("first reading is not at the middle of the window:\n%s", svg)
	}
	if !strings.Contains(svg, "H700.00 ") {
		t.Errorf("the latest value does not hold to the window's end:\n%s", svg)
	}
	// One dot: the latest reading. Neither run is a lone reading.
	if n := strings.Count(svg, "<circle"); n != 1 {
		t.Errorf("%d dots, want 1 (the latest reading)", n)
	}
}

// A series whose readings stopped long ago does not pretend its last
// value is still current.
func TestBuildChartSVG_StaleLastReadingDoesNotHoldToTheEnd(t *testing.T) {
	to := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	points := readings(from.Add(time.Hour), time.Hour, 6, 5)
	svg := string(buildChartSVG(points, "neutral", "count", from, to, 700, 100).SVG)
	if strings.Contains(svg, "H700.00") {
		t.Fatalf("a stale series holds to the window's end:\n%s", svg)
	}
}

func TestGapThreshold(t *testing.T) {
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
	cases := []struct {
		v    float64
		unit string
		want string
	}{
		{92.345, "%", "92.3%"},
		{4200, "ms", "4200ms"},
		{15, "minutes", "15m"},
		{87, "score", "87"},
		{42, "count", "42"},
	}
	for _, c := range cases {
		if got := formatMetricValue(c.v, c.unit); got != c.want {
			t.Errorf("formatMetricValue(%v, %q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

func TestResolveChartRange(t *testing.T) {
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
