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
	if got := buildChartSVG(nil, "higher_better", "%", 100, 40); got.HasData {
		t.Error("HasData = true for nil points")
	}
	allEmpty := []repo.Point{pt(2, 0, false), pt(1, 0, false), pt(0, 0, false)}
	if got := buildChartSVG(allEmpty, "higher_better", "%", 100, 40); got.HasData {
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
	got := buildChartSVG(points, "higher_better", "%", 500, 100)
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
	got := buildChartSVG(points, "higher_better", "count", 300, 80)
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
	got := buildChartSVG(points, "higher_better", "%", 500, 100)
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

	if got := buildChartSVG(rising, "higher_better", "%", 200, 60); got.Tone != "pass" {
		t.Errorf("coverage (higher_better) rising: Tone = %q, want pass", got.Tone)
	}
	if got := buildChartSVG(falling, "higher_better", "%", 200, 60); got.Tone != "fail" {
		t.Errorf("coverage (higher_better) falling: Tone = %q, want fail", got.Tone)
	}
	if got := buildChartSVG(rising, "lower_better", "count", 200, 60); got.Tone != "fail" {
		t.Errorf("CVE count (lower_better) rising: Tone = %q, want fail", got.Tone)
	}
	if got := buildChartSVG(falling, "lower_better", "count", 200, 60); got.Tone != "pass" {
		t.Errorf("CVE count (lower_better) falling: Tone = %q, want pass", got.Tone)
	}
	if got := buildChartSVG(rising, "neutral", "count", 200, 60); got.Tone != "none" {
		t.Errorf("neutral direction: Tone = %q, want none regardless of trend", got.Tone)
	}
}

func TestBuildChartSVG_FlatSeriesDoesNotPanicOnZeroRange(t *testing.T) {
	flat := []repo.Point{pt(2, 50, true), pt(1, 50, true), pt(0, 50, true)}
	got := buildChartSVG(flat, "higher_better", "%", 200, 60)
	if !got.HasData || got.Tone != "none" {
		t.Errorf("flat series: HasData=%v Tone=%q, want HasData=true Tone=none (no delta)", got.HasData, got.Tone)
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
		"1y":    {"1y", 365 * 24 * time.Hour},
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
	// task 4.6's acceptance criterion that 7d/30d/1y "beneran muat
	// resolusi yang beda" depends entirely on each width landing on a
	// different repo.Series tier (raw/hourly/daily) — this pins that
	// invariant at the boundary repo.Series itself defines (9d raw
	// ceiling, 40d hourly ceiling), so a change to either constant
	// breaks a test instead of silently producing three same-resolution
	// charts.
	from7, to7, _ := resolveChartRange("7d")
	from30, to30, _ := resolveChartRange("30d")
	from1y, to1y, _ := resolveChartRange("1y")

	if w := to7.Sub(from7); w > 9*24*time.Hour {
		t.Errorf("7d width = %v, want <= 9d (repo.Series' raw-tier ceiling) so it reads raw, not hourly", w)
	}
	if w := to30.Sub(from30); w <= 9*24*time.Hour || w > 40*24*time.Hour {
		t.Errorf("30d width = %v, want in (9d, 40d] (repo.Series' hourly tier)", w)
	}
	if w := to1y.Sub(from1y); w <= 40*24*time.Hour {
		t.Errorf("1y width = %v, want > 40d (repo.Series' daily tier)", w)
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
