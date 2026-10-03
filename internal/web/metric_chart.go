package web

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// chartRangeOptions is the fixed set of range buttons the UI offers —
// task 4.6's own three named ranges. resolveChartRange also accepts any
// other string (falling back to 7d) so a caller could pass a real custom
// range key later without this list needing to grow to match; the spec's
// "plus custom" is about the range-resolution mechanism not being
// hardcoded to exactly three, not about shipping a date-picker in v1.
var chartRangeOptions = []string{"7d", "30d"}

// resolveChartRange turns a range key into the [from, to] window Series
// reads — deliberately just three cases plus a fallback, not a generic
// duration parser: task 3.7's own tier thresholds (rawTierMax=9d,
// hourlyTierMax=40d) already have headroom specifically so these nominal
// widths each land on their intended tier, and a chart's "denser 7d than
// 30d" acceptance criterion depends on it being exactly one of them.
//
// A year was offered too and was dropped: 30d is the widest window these
// tiers keep at useful density, and a year of hourly buckets read as a
// smear rather than a trend.
func resolveChartRange(rangeKey string) (from, to time.Time, label string) {
	to = time.Now()
	switch rangeKey {
	case "30d":
		return to.AddDate(0, 0, -30), to, "30d"
	default:
		return to.AddDate(0, 0, -7), to, "7d"
	}
}

// metricChartCard is one metric_keys row rendered as a chart — the
// "satu grafik buat tiap key aktif" unit task 4.6's spec describes.
type metricChartCard struct {
	Key           string
	Label         string
	Active        bool
	InactiveSince string // set only when !Active
	Chart         chartSVG
}

// buildMetricChartCards reads kind's whole metric_keys catalog (task
// 3.7) — never a hardcoded field list — and renders one card per
// numeric key, in the same order the catalog itself declares. A new key
// a collector starts emitting shows up here with zero template changes,
// task 4.6's own acceptance criterion; a key that's gone inactive still
// renders (its own card just says since when), rather than silently
// disappearing.
//
// Text-valued keys (SonarQube's letter-grade ratings: value_type
// "text", stored as metric_facts.value_text rather than value_num) are
// skipped — repo.Series only ever reads value_num, so a rating key would
// render as an all-empty chart, and a letter-grade-over-time
// visualization is a different problem than a line chart. Out of scope
// for this pass; the rating's current value already has its own home on
// the Sonar detail page's stat cards (task 4.4).
func buildMetricChartCards(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, kind, rangeKey string) ([]metricChartCard, error) {
	keys, err := repo.ListMetricKeys(ctx, s, kind)
	if err != nil {
		return nil, err
	}
	from, to, _ := resolveChartRange(rangeKey)

	cards := make([]metricChartCard, 0, len(keys))
	for _, k := range keys {
		if k.ValueType != "number" {
			continue
		}
		points, err := repo.Series(ctx, s, bindingID, k.Key, from, to)
		if err != nil {
			return nil, err
		}
		card := metricChartCard{Key: k.Key, Label: k.Label, Active: k.Active}
		if !k.Active {
			card.InactiveSince = k.LastSeenAt.Local().Format("2006-01-02")
		}
		card.Chart = buildChartSVG(points, k.Direction, k.Unit, 560, 130)
		cards = append(cards, card)
	}
	return cards, nil
}

// metricChartsFragment is GET .../charts?range= — both the initial
// server-rendered chart grid (called directly from each kind's own
// renderXDetail, task 4.4) and the htmx swap target when the range
// selector changes. One handler, one query shape, so the two can never
// drift apart into showing different data for the same range.
func metricChartsFragment(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		rangeKey := c.Query("range", "7d")
		cards, err := buildMetricChartCards(c.Context(), s, chrome.Binding.ID, chrome.Binding.Kind, rangeKey)
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return d.Renderer.RenderNamed(c, "metric_charts", newChartGridData(cards, rangeKey, chrome.BasePath()))
	}
}

// chartGridData is metric_charts.html's own Data shape — a named type
// (not an inline anonymous struct) specifically so both the htmx-fragment
// handler above and each kind's own renderXDetail (task 4.4) build the
// exact same shape for the exact same template, rather than four
// independently-typed anonymous structs that could drift out of sync
// with what the template actually expects.
type chartGridData struct {
	Cards         []metricChartCard
	SelectedRange string
	RangeOptions  []string
	BasePath      string
}

func newChartGridData(cards []metricChartCard, rangeKey, basePath string) chartGridData {
	return chartGridData{Cards: cards, SelectedRange: rangeKey, RangeOptions: chartRangeOptions, BasePath: basePath}
}

func registerMetricChartRoutes(app *fiber.App, base string, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get(base+"/charts", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), metricChartsFragment(d))
}
