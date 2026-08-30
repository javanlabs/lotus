package web

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// scoringDocsPage explains how the Lotus score is computed, for someone
// who has just been handed a number between 0 and 100 and wants to know
// what to do about it.
//
// The page is static prose, but it deliberately lives behind the tenant
// session rather than being a public marketing page: it names the exact
// thresholds this deployment scores against, and those are operational
// details of the organization's own setup.
func scoringDocsPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
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

		c.Type("html")
		return renderPage(c, d, "pages/scoring.html", httpx.ViewData{
			Title: "How scoring works", Org: org, User: user,
			Nav: buildNav("scoring", len(projects)),
		})
	}
}

func registerScoringDocsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/scoring", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), scoringDocsPage(d))
}
