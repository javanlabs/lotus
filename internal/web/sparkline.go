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
		key := primaryChartKey(binding.Kind)
		var chart chartSVG
		if key != "" {
			keys, err := repo.ListMetricKeys(c.Context(), s, binding.Kind)
			if err != nil {
				return writeRepoErr(c, err)
			}
			var direction, unit string
			found := false
			for _, k := range keys {
				if k.Key == key {
					direction, unit, found = k.Direction, k.Unit, true
					break
				}
			}
			if found {
				// 7d, not 30d: a compact row glance is about the most
				// recent trend, and 7d reads the raw metric_facts tier
				// directly (repo.Series) rather than the hourly rollup —
				// a fact just collected shows up immediately, with no
				// dependency on the rollup job having run yet.
				from, to, _ := resolveChartRange("7d")
				points, err := repo.Series(c.Context(), s, id, key, from, to)
				if err != nil {
					return writeRepoErr(c, err)
				}
				chart = buildChartSVG(points, direction, unit, 90, 28)
			}
		}
		c.Type("html")
		return d.Renderer.RenderPartial(c, "partials/binding_sparkline.html", chart)
	}
}

func registerSparklineRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/bindings/:id/sparkline", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), bindingSparklineFragment(d))
}
