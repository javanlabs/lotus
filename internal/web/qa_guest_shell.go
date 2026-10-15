package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// A guest's whole Lotus is the QA of the projects it is listed on: its
// home is /qa/my, its sidebar is My work plus one entry per project, and
// every page outside QA answers 404 (authz.Require).

// guestHome sends a guest from the landing page (/projects, where / and
// every login lead) to its own work.
//
// Only while LOTUS_FEATURE_QA is on. A guest exists only for QA, so with
// the module off it has nowhere to go; it falls through to the landing
// page's own permission check and gets the same not-found as any other
// page it may not read, rather than a redirect to a route that is not
// registered.
func guestHome(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !d.Cfg.Features.QA {
			return c.Next()
		}
		if p, ok := authz.CurrentPrincipal(c); ok && authz.IsProjectScoped(p) {
			return c.Redirect("/qa/my", fiber.StatusFound)
		}
		return c.Next()
	}
}

// guestNav replaces the sidebar for a guest. Pages build the one everybody
// else sees; renderPage trims it here so no page has to remember to.
func guestNav(c *fiber.Ctx, nav *httpx.Nav) {
	p, ok := authz.CurrentPrincipal(c)
	if !ok || !authz.IsProjectScoped(p) {
		return
	}
	nav.Guest = true
	nav.FooterItems = nil
	nav.Items = []httpx.NavItem{{Key: "qa_tools", Label: "My work", Href: "/qa/my", Icon: iconQATools}}
	nav.Active = "qa_tools"
	s, ok := tenant.FromContext(c)
	if !ok {
		return
	}
	ids, err := repo.ListProjectIDsForMember(c.Context(), s, p.UserID)
	if err != nil || len(ids) == 0 {
		return
	}
	listed := make(map[string]bool, len(ids))
	for _, id := range ids {
		listed[id.String()] = true
	}
	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return
	}
	for _, project := range projects {
		if !listed[project.ID.String()] {
			continue
		}
		key := "qa_project_" + project.ID.String()
		href := qaBase(&project)
		nav.Items = append(nav.Items, httpx.NavItem{Key: key, Label: project.Name, Href: href, Icon: iconProjects})
		if c.Path() == href || strings.HasPrefix(c.Path(), href+"/") {
			nav.Active = key
		}
	}
}
