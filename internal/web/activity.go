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
// activityPerPage is the feed's page size. The list is scannable rather
// than searchable, so a page is sized to a screenful, not to a round
// number of records.
const activityPerPage = 25

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
		total, err := global.CountRunsForOrg(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		page := c.QueryInt("page", 1)
		pages := (total + activityPerPage - 1) / activityPerPage
		if pages < 1 {
			pages = 1
		}
		// Clamped rather than 404'd: a stale bookmark to page 9 after runs
		// aged out should show the last page, not an error.
		if page < 1 {
			page = 1
		}
		if page > pages {
			page = pages
		}
		runs, err := global.ListRunsForOrgPage(c.Context(), d.DB, chrome.OrgID,
			activityPerPage, (page-1)*activityPerPage)
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
		return renderPage(c, d, "pages/activity.html", httpx.ViewData{
			Title: "Activity", Org: org, User: user, Nav: nav,
			Data: struct {
				Runs     []activityRow
				Page     int
				Pages    int
				Total    int
				PrevPage int
				NextPage int
				From     int
				To       int
			}{
				Runs: rows, Page: page, Pages: pages, Total: total,
				PrevPage: page - 1, NextPage: page + 1,
				From: (page-1)*activityPerPage + 1, To: (page-1)*activityPerPage + len(rows),
			},
		})
	}
}

func registerActivityRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/activity", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), activityPage(d))
}
