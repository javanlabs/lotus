package web

import (
	"fmt"
	"github.com/uyab/lotus/internal/repo"
	"html/template"

	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// navIcons are the sidebar's 24x24 stroke icons, lifted verbatim from the
// Claude Design mockup (Lotus Dashboard.dc.html) so the app shell matches
// it exactly. Kept as constants rather than files: each is a small,
// static <svg> fragment, never templated with data.
const (
	// iconDashboard is unused while the landing page is the projects list;
	// kept for the dashboard that replaces it later rather than deleted
	// and re-drawn.
	iconDashboard   = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:currentColor;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="square" stroke-linejoin="round" stroke-width="1.5" d="M13.69 19.457c-.19-.46-.19-1.042-.19-2.207s0-1.747.19-2.207a2.5 2.5 0 0 1 1.353-1.353c.46-.19 1.042-.19 2.207-.19s1.747 0 2.207.19a2.5 2.5 0 0 1 1.353 1.353c.19.46.19 1.042.19 2.207s0 1.747-.19 2.207a2.5 2.5 0 0 1-1.353 1.353c-.46.19-1.042.19-2.207.19s-1.747 0-2.207-.19a2.5 2.5 0 0 1-1.353-1.353Zm0-10.5c-.19-.46-.19-1.042-.19-2.207s0-1.747.19-2.207a2.5 2.5 0 0 1 1.353-1.353C15.503 3 16.085 3 17.25 3s1.747 0 2.207.19a2.5 2.5 0 0 1 1.353 1.353c.19.46.19 1.042.19 2.207s0 1.747-.19 2.207a2.5 2.5 0 0 1-1.353 1.353c-.46.19-1.042.19-2.207.19s-1.747 0-2.207-.19a2.5 2.5 0 0 1-1.353-1.353Zm-10.5 10.5C3 18.997 3 18.415 3 17.25s0-1.747.19-2.207a2.5 2.5 0 0 1 1.353-1.353c.46-.19 1.042-.19 2.207-.19s1.747 0 2.207.19a2.5 2.5 0 0 1 1.353 1.353c.19.46.19 1.042.19 2.207s0 1.747-.19 2.207a2.5 2.5 0 0 1-1.353 1.353c-.46.19-1.042.19-2.207.19s-1.747 0-2.207-.19a2.5 2.5 0 0 1-1.353-1.353Zm0-10.5C3 8.497 3 7.915 3 6.75s0-1.747.19-2.207A2.5 2.5 0 0 1 4.543 3.19C5.003 3 5.585 3 6.75 3s1.747 0 2.207.19a2.5 2.5 0 0 1 1.353 1.353c.19.46.19 1.042.19 2.207s0 1.747-.19 2.207a2.5 2.5 0 0 1-1.353 1.353c-.46.19-1.042.19-2.207.19s-1.747 0-2.207-.19A2.5 2.5 0 0 1 3.19 8.957Z"></path></svg>`
	iconProjects    = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-width="1.5" d="M8 7h8.75c2.107 0 3.16 0 3.917.506a3 3 0 0 1 .827.827C22 9.09 22 10.143 22 12.25c0 3.511 0 5.267-.843 6.528a5 5 0 0 1-1.38 1.38C18.518 21 16.762 21 13.25 21H12c-4.714 0-7.071 0-8.536-1.465C2 18.072 2 15.715 2 11V7.944c0-1.816 0-2.724.38-3.406A3 3 0 0 1 3.538 3.38C4.22 3 5.128 3 6.944 3C8.108 3 8.69 3 9.2 3.191c1.163.436 1.643 1.493 2.168 2.542L12 7"></path></svg>`
	iconIntegration = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-width="1.5"><path d="M17.854 12.16c-.383.45-1.09.454-1.537.007l-4.484-4.483c-.447-.447-.444-1.155.007-1.538l1.231-1.047a6.5 6.5 0 0 1 3.133-1.448l.725-.122c.685-.116 1.405.123 1.919.637l.986.987c.514.513.753 1.233.637 1.918l-.122.725a6.5 6.5 0 0 1-1.448 3.133z"></path><path stroke-linecap="round" stroke-linejoin="round" d="m19.5 4.5l2-2m-19 19l2-2"></path><path d="M6.146 11.84c.383-.45 1.09-.454 1.538-.007l4.483 4.484c.447.446.444 1.154-.007 1.537l-1.231 1.047a6.5 6.5 0 0 1-3.133 1.448l-.725.122c-.685.116-1.405-.123-1.918-.637l-.987-.986c-.514-.514-.753-1.234-.637-1.919l.122-.725a6.5 6.5 0 0 1 1.448-3.133z"></path><path stroke-linecap="round" stroke-linejoin="round" d="m8.5 12.5l2-2m1 5l2-2"></path></g></svg>`
	iconMembers     = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"></path><circle cx="9" cy="7" r="4"></circle><path d="M22 21v-2a4 4 0 0 0-3-3.87m-3-12a4 4 0 0 1 0 7.75"></path></g></svg>`
	iconActivity    = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M22 12h-4l-3 9L9 3l-3 9H2"></path></svg>`
)

// buildNav assembles the sidebar for the given active item. Every metric
// is read through a project (Projects), so the sidebar has no per-metric
// entries; QA Tools is the one cross-project area, because QA work (and a
// guest's whole view) spans projects.
func buildNav(active string, projectCount int) *httpx.Nav {
	return &httpx.Nav{
		Active: active,
		Items: []httpx.NavItem{
			// Projects is the landing page until there is a dashboard worth
			// the name; the old "Dashboard" entry pointed at this same list
			// under a different label, which only invited the question of
			// what the difference was.
			{Key: "projects", Label: "Projects", Href: "/projects", Count: fmt.Sprintf("%d", projectCount), Icon: template.HTML(iconProjects)}, //nolint:gosec // constant, not user input
			{Key: "qa_tools", Label: "QA Tools", Href: "/qa", Icon: template.HTML(iconQATools)},                                                //nolint:gosec // constant, not user input
			{Key: "integrations", Label: "Integrations", Href: "/settings/connections", Icon: template.HTML(iconIntegration)},
			{Key: "members", Label: "Members", Href: "/settings/members", Icon: template.HTML(iconMembers)},
			{Key: "activity", Label: "Activity", Href: "/activity", Icon: template.HTML(iconActivity)},
		},
		FooterItems: []httpx.NavItem{
			{Key: "scoring", Label: "How scoring works", Href: "/scoring", Icon: template.HTML(iconScoring)}, //nolint:gosec // constant, not user input
		},
	}
}

// shellChrome is the Org/User/Nav trio every authenticated tenant page
// needs for httpx.ViewData. Loaded once per request from the principal
// LoadPrincipal already resolved and the org the host already resolved to
// — every shell page handler calls this instead of re-deriving it.
func shellChrome(c *fiber.Ctx, db *sqlx.DB) (*httpx.Org, *httpx.User, error) {
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return nil, nil, fiber.ErrNotFound
	}
	principal, ok := authz.CurrentPrincipal(c)
	if !ok {
		return nil, nil, fiber.NewError(fiber.StatusUnauthorized, "no principal")
	}
	user, err := global.UserByID(c.Context(), db, principal.UserID)
	if err != nil {
		return nil, nil, err
	}
	return &httpx.Org{Name: org.Name, Subdomain: org.Subdomain}, &httpx.User{
		Name: user.Name, Email: derefOr(user.Email, ""), Role: principal.Role,
	}, nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// iconScoring: a question mark in a circle — this is the "explain the
// number" entry, sitting last in the nav where help conventionally lives.
const iconScoring = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="flex:none"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"><circle cx="12" cy="12" r="9"></circle><path d="M10.125 9.5a1.875 1.875 0 1 1 2.62 1.72c-.58.26-.745.79-.745 1.28v.5"></path><path d="M12 16.5v.01"></path></g></svg>`

// navWithTrail is buildNav plus a breadcrumb path, for the pages that sit
// below the organization root.
func navWithTrail(active string, projectCount int, trail []httpx.NavCrumb) *httpx.Nav {
	nav := buildNav(active, projectCount)
	nav.Trail = trail
	return nav
}

// projectTrail is the breadcrumb path for a project page — the same path
// the folder view shows: every folder down to the project, then the
// project itself as the current page.
func projectTrail(folders []repo.Folder, project *repo.Project) []httpx.NavCrumb {
	return append(projectFolderPath(folders, project), httpx.NavCrumb{Label: project.Name})
}

// componentTrail is the path for a component's own page — the project's
// folder path, then project → component. It is drawn only for a component
// with nothing bound; one with a binding opens on a metric page, whose
// path is metricTrail.
func componentTrail(folders []repo.Folder, project *repo.Project, component *repo.Component) []httpx.NavCrumb {
	return append(projectFolderPath(folders, project),
		httpx.NavCrumb{Label: project.Name, Href: projectURL(project)},
		httpx.NavCrumb{Label: component.Name})
}

// metricTrail is the path for a component's page for one kind, and
// anything below it: project → the project's page for the kind → the
// component → whatever the caller adds (Syncs).
//
// Kind before component, so the path is the same whichever way the page
// was reached. From the project's SonarQube page a row opens the
// component's SonarQube tab, and the trail only grows by the component;
// the SonarQube crumb before it leads back to every component's
// SonarQube, side by side. A component-first path (project → component →
// SonarQube) dropped that page from the trail and named a different page
// "SonarQube" at its end.
func metricTrail(folders []repo.Folder, project *repo.Project, component *repo.Component, kind string, deeper ...httpx.NavCrumb) []httpx.NavCrumb {
	trail := projectKindTrail(folders, project, kind)
	trail[len(trail)-1].Href = projectMetricURL(project, kind)
	if len(deeper) == 0 {
		return append(trail, httpx.NavCrumb{Label: component.Name})
	}
	trail = append(trail, httpx.NavCrumb{Label: component.Name, Href: metricURL(project, component, kind)})
	return append(trail, deeper...)
}

// projectKindTrail is the path for a project's page for one kind: the
// project's folder path, the project, then the kind as the current page.
// metricTrail starts with it, so a component's page for a kind always
// sits under the project's.
func projectKindTrail(folders []repo.Folder, project *repo.Project, kind string) []httpx.NavCrumb {
	return append(projectFolderPath(folders, project),
		httpx.NavCrumb{Label: project.Name, Href: projectURL(project)},
		httpx.NavCrumb{Label: kindLabels[kind]})
}

// projectFolderPath is every folder the project sits in, outermost first,
// each linking to that folder in the folder view. The top level needs no
// crumb of its own: the organization's name before the trail links there.
func projectFolderPath(folders []repo.Folder, project *repo.Project) []httpx.NavCrumb {
	crumbs := newFolderView(nil, folders, nil, parentKey(project.FolderID)).crumbs()
	out := make([]httpx.NavCrumb, 0, len(crumbs)-1)
	for _, c := range crumbs[1:] {
		out = append(out, httpx.NavCrumb{Label: c.Name, Href: c.Href})
	}
	return out
}

// iconQATools: a clipboard with a check — test plans and runs.
const iconQATools = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:currentColor;flex:none"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"><path d="M9 4H7.5A2.5 2.5 0 0 0 5 6.5v12A2.5 2.5 0 0 0 7.5 21h9a2.5 2.5 0 0 0 2.5-2.5v-12A2.5 2.5 0 0 0 16.5 4H15"></path><rect x="9" y="2.5" width="6" height="3" rx="1"></rect><path d="m9.5 13.5l1.75 1.75L14.5 12"></path></g></svg>`
