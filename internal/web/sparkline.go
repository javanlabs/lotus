package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// primaryChartKey is each kind's own "headline" metric_keys entry — the
// one worth a compact glance on the component page's row (task 4.6's own
// "sparkline ringkas buat metrik utama tiap binding"). There's no
// "primary" flag in the metric_keys catalog itself (task 3.7 declared
// none), so this is a UI-layer opinion, not a schema fact — picked to
// match each kind's own most-prominent stat on its full detail page
// (task 4.4): GitLab's pipeline health, Sonar's coverage, Trivy's total
// finding count, Sentry's health score.
func primaryChartKey(kind string) string {
	switch kind {
	case "gitlab":
		return "gitlab.pipeline_success_rate"
	case "sonarqube":
		return "sonar.coverage"
	case "trivy":
		return "trivy.total"
	case "sentry":
		return "sentry.score"
	default:
		return ""
	}
}

// bindingSparklineFragment is GET /bindings/:id/sparkline — htmx-loaded
// (hx-trigger="load") from each row on the component page, not fetched
// eagerly by componentPage's own handler: a component can have many
// bindings, and running one repo.Series query per row synchronously in
// the page's own request would turn "open a component" into an N+1 query
// fan-out. Loading each row's sparkline as its own small follow-up
// request keeps the page's first paint to exactly the one query
// componentPage already runs.
func bindingSparklineFragment(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		binding, err := repo.BindingByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		chart, err := bindingChart(c, s, id, binding.Kind, "7d")
		if err != nil {
			return err
		}
		c.Type("html")
		return d.Renderer.RenderPartial(c, "partials/binding_sparkline.html", chart)
	}
}

// componentSparklineFragment backs the repositories table's "TREND · 14D"
// column. A component has several bindings, and there is no stored history
// of its rolled-up score to draw, so this shows the trend of its most
// telling single binding instead — GitLab first, then SonarQube, Sentry,
// Trivy. That is a proxy, not the component's own score over time; a true score
// series would need a history table that does not exist yet.
func componentSparklineFragment(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		bindings, err := repo.ListBindingsForComponent(c.Context(), s, id, false)
		if err != nil {
			return writeRepoErr(c, err)
		}

		var chart chartSVG
		if b := preferredTrendBinding(bindings); b != nil {
			chart, err = bindingChart(c, s, b.ID, b.Kind, "30d")
			if err != nil {
				return err
			}
		}
		c.Type("html")
		return d.Renderer.RenderPartial(c, "partials/binding_sparkline.html", chart)
	}
}

// trendKindPreference orders the integrations by how well a single one
// stands in for a component's overall direction of travel.
var trendKindPreference = []string{"gitlab", "sonarqube", "sentry", "trivy"}

func preferredTrendBinding(bindings []repo.Binding) *repo.Binding {
	for _, want := range trendKindPreference {
		for i := range bindings {
			if bindings[i].Kind == want && bindings[i].Enabled {
				return &bindings[i]
			}
		}
	}
	return nil
}

func registerSparklineRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/bindings/:id/sparkline", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), bindingSparklineFragment(d))
	app.Get("/components/:id/sparkline", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), componentSparklineFragment(d))
}

// bindingChart renders one binding's primary metric over rangeKey. Shared
// by the per-binding sparkline (7d — a compact row glance is about the most
// recent trend, and 7d reads the raw metric_facts tier directly, so a fact
// just collected shows up without waiting on the rollup job) and the
// component trend column (30d, matching the mockup's wider window).
func bindingChart(c *fiber.Ctx, s tenant.Scope, bindingID uuid.UUID, kind, rangeKey string) (chartSVG, error) {
	key := primaryChartKey(kind)
	if key == "" {
		return chartSVG{}, nil
	}
	keys, err := repo.ListMetricKeys(c.Context(), s, kind)
	if err != nil {
		return chartSVG{}, writeRepoErr(c, err)
	}
	var direction, unit string
	found := false
	for _, k := range keys {
		if k.Key == key {
			direction, unit, found = k.Direction, k.Unit, true
			break
		}
	}
	if !found {
		return chartSVG{}, nil
	}
	from, to, _ := resolveChartRange(rangeKey)
	points, err := repo.Series(c.Context(), s, bindingID, key, from, to)
	if err != nil {
		return chartSVG{}, writeRepoErr(c, err)
	}
	return buildChartSVG(points, direction, unit, 90, 28), nil
}
