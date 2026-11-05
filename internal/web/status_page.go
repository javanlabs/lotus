package web

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The public status page, GET /status/<project-slug>?date=YYYY-MM-DD
// (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §6):
// a project's verdict for one day, the 30 days before it and its
// components, for a client. No session; it answers only for a live project
// whose owner or admin published it (projects.status_page_enabled_at).
// Everything else — an unknown slug, a deleted project, a page turned off,
// another organization's project (RLS) — is the same 404, so a guessed
// slug reveals nothing.
//
// The page shows what the recorded days hold and nothing more: no issue
// lists, messages, connections, links into the app, people or tags.

func statusPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Before anything can answer, so a 404 is the same response for
		// every reason. no-store: turning the page off must take effect
		// at once, not when a cache expires.
		c.Set("Cache-Control", "no-store")
		c.Set("X-Robots-Tag", "noindex, nofollow")
		c.Set("Referrer-Policy", "no-referrer")

		s, err := mustScope(c)
		if err != nil {
			return err
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		project, err := repo.ProjectBySlug(c.Context(), s, c.Params("slug"))
		if errors.Is(err, repo.ErrNotFound) || (err == nil && project.StatusPageEnabledAt == nil) {
			return fiber.ErrNotFound
		}
		if err != nil {
			return err
		}

		selected := parseStatusDate(c.Query("date"), time.Now())
		days, err := repo.ProjectStatusDays(c.Context(), s, project.ID,
			selected.AddDate(0, 0, -(statusStripDays-1)), selected)
		if err != nil {
			return err
		}
		view := buildStatusView(org.Name, project, days, selected)

		c.Type("html")
		return d.Renderer.Render(c, "pages/status.html", httpx.ViewData{
			Title: project.Name + " · Quality report", Data: view,
		})
	}
}

func registerStatusPageRoutes(app *fiber.App, d Deps) {
	app.Get("/status/:slug", tenant.RequireTenant(),
		authz.Public("public status page: answers only for a project whose owner/admin published it"),
		statusPage(d))
}
