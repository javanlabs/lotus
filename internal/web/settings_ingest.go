package web

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// ingestBindingRow is one push-ingestible binding, with its full ingest
// URL already assembled — task 4.5's spec: "siap di-copy ke CI", so the
// page hands over a complete, pasteable URL rather than a template the
// reader has to fill in themselves.
type ingestBindingRow struct {
	ProjectName, ComponentName, ExternalKey, URL string
}

// registerIngestSettingsRoutes' page is read-only documentation — there's
// only one push-ingest route today (POST /ingest/trivy/:binding_id, task
// 3.4); this page lists every trivy binding's own URL plus the curl
// snippet CI needs, it doesn't add any new capability of its own.
func settingsIngestPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		orgCtx, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		bindings, err := repo.ListBindingsForOrgByKind(c.Context(), s, "trivy")
		if err != nil {
			return writeRepoErr(c, err)
		}
		scheme := "https"
		if d.Cfg.Dev {
			scheme = "http"
		}
		rows := make([]ingestBindingRow, 0, len(bindings))
		for _, b := range bindings {
			rows = append(rows, ingestBindingRow{
				ProjectName: b.ProjectName, ComponentName: b.ComponentName, ExternalKey: b.ExternalKey,
				URL: scheme + "://" + orgCtx.Subdomain + "." + d.Cfg.BaseDomain + "/ingest/trivy/" + b.ID.String(),
			})
		}

		nav := buildNav("", len(projects))
		nav.Trail = []httpx.NavCrumb{{Label: "Settings"}}
		c.Type("html")
		return renderPage(c, d, "pages/settings_ingest.html", httpx.ViewData{
			Title: "Ingest", Org: org, User: user, Nav: nav,
			Data: struct {
				SettingsNav []settingsNavItem
				Bindings    []ingestBindingRow
			}{SettingsNav: buildSettingsNav("ingest"), Bindings: rows},
		})
	}
}

func registerIngestSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/settings/ingest", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.read"), settingsIngestPage(d))
}
