package web

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// activityRow is one sync_runs entry for the org-wide feed — reuses
// task 4.4's own runRow shape/tone mapping (metric_detail.go) rather than
// a second copy of it, plus the run's own kind, since this feed spans
// every binding in the org at once, not one binding's own history.
type activityRow struct {
	runRow
	Kind string
}

func activityPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		runs, err := global.ListRunsForOrg(c.Context(), d.DB, chrome.OrgID, 100)
		if err != nil {
			return writeRepoErr(c, err)
		}
		baseRows := buildRunRows(runs)
		rows := make([]activityRow, len(runs))
		for i, r := range runs {
			rows[i] = activityRow{runRow: baseRows[i], Kind: r.Kind}
		}

		nav := buildNav("activity", len(projects))
		c.Type("html")
		return d.Renderer.Render(c, "pages/activity.html", httpx.ViewData{
			Title: "Activity", Org: org, User: user, Nav: nav,
			Data: struct {
				Runs []activityRow
			}{Runs: rows},
		})
	}
}

func registerActivityRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/activity", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), activityPage(d))
}
