package web

import (
	"encoding/json"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The publish switch for a project's public status page
// (components/status_publish.html, on the project page). Off by default;
// only owners and admins (project.status_page) turn it on or off. Turning
// it off makes /status/<slug> answer 404 at once.

// statusPageData is what the status_publish partial renders: whether the
// page is published, its public URL, and whether this person may switch it.
type statusPageData struct {
	On  bool
	URL string
	Can bool
}

// statusPageFor is the project page's switch. The URL is the
// organization's own origin, as share links are built (qa_share.go), so it
// can be sent to a client as is.
func statusPageFor(c *fiber.Ctx, d Deps, project *repo.Project) statusPageData {
	data := statusPageData{On: project.StatusPageEnabledAt != nil, Can: mustPrincipal(c).Can("project.status_page")}
	if org, ok := tenant.OrgFromContext(c); ok {
		data.URL = d.Cfg.TenantURL(org.Subdomain) + "/status/" + project.Slug
	}
	return data
}

func setStatusPageUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return writeRepoErr(c, err)
		}
		on := c.FormValue("enabled") == "1"
		if err := repo.SetProjectStatusPage(c.Context(), s, project.ID, on); err != nil {
			return writeRepoErr(c, err)
		}
		action, flash := "project.status_page.disable", "project.status_page.off"
		if on {
			action, flash = "project.status_page.enable", "project.status_page.on"
		}
		meta, _ := json.Marshal(fiber.Map{"slug": project.Slug})
		if err := repo.RecordAuditEvent(c.Context(), s, auditActor(c), action, "project", &project.ID, meta); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, flash)
		// Back with the settings popover open, so the URL just published
		// (and its Copy button) is in view.
		return redirectAfterMutation(c, projectURL(project)+"#settings")
	}
}

func registerStatusPublishRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post("/projects/:id/status-page", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.status_page"), setStatusPageUI(d))
}
