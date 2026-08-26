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
	iconSecurity    = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M18.709 3.495C16.817 2.554 14.5 2 12 2s-4.816.554-6.709 1.495c-.928.462-1.392.693-1.841 1.419S3 6.342 3 7.748v3.49c0 5.683 4.542 8.842 7.173 10.196c.734.377 1.1.566 1.827.566s1.093-.189 1.827-.566C16.457 20.08 21 16.92 21 11.237V7.748c0-1.406 0-2.108-.45-2.834s-.913-.957-1.841-1.419"></path></svg>`
	iconCodeQuality = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="m17 8l1.84 1.85c.773.778 1.16 1.167 1.16 1.65s-.387.872-1.16 1.65L17 15M7 8L5.16 9.85C4.387 10.628 4 11.017 4 11.5s.387.872 1.16 1.65L7 15m7.5-11l-5 16"></path></svg>`
	iconErrors      = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"><path d="M13.925 21h-3.85c-4.63 0-6.945 0-7.799-1.506c-.853-1.506.331-3.503 2.7-7.495L6.9 8.753C9.176 4.918 10.313 3 12 3s2.824 1.918 5.1 5.753L19.023 12c2.369 3.992 3.553 5.989 2.7 7.495C20.87 21 18.555 21 13.924 21M12 9v4"></path><path d="M12.125 16.75H12m.25 0a.25.25 0 1 1-.5 0a.25.25 0 0 1 .5 0"></path></g></svg>`
	iconPerformance = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" d="M13.5 13L17 9m-3 6a2 2 0 1 1-4 0a2 2 0 0 1 4 0Zm-8-3a6 6 0 0 1 9-5.197"></path><path d="M2.5 12c0-4.478 0-6.717 1.391-8.109c1.392-1.39 3.63-1.39 8.11-1.39c4.477 0 6.717 0 8.108 1.39c1.391 1.392 1.391 3.63 1.391 8.11c0 4.477 0 6.717-1.391 8.108S16.479 21.5 12 21.5c-4.478 0-6.717 0-8.109-1.391c-1.39-1.391-1.39-3.63-1.39-8.109Z"></path></g></svg>`
	iconGitHygiene  = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M7 19h6c2.828 0 4.243 0 5.121-.879C19 17.243 19 15.828 19 13v-3m0 0c.7 0 2.009 1.994 2.5 2.5M19 10c-.7 0-2.009 1.994-2.5 2.5M5 7v10"></path><circle cx="5" cy="5" r="2"></circle><circle cx="19" cy="5" r="2"></circle><circle cx="5" cy="19" r="2"></circle></g></svg>`
	iconIntegration = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><g fill="none" stroke="currentColor" stroke-width="1.5"><path d="M17.854 12.16c-.383.45-1.09.454-1.537.007l-4.484-4.483c-.447-.447-.444-1.155.007-1.538l1.231-1.047a6.5 6.5 0 0 1 3.133-1.448l.725-.122c.685-.116 1.405.123 1.919.637l.986.987c.514.513.753 1.233.637 1.918l-.122.725a6.5 6.5 0 0 1-1.448 3.133z"></path><path stroke-linecap="round" stroke-linejoin="round" d="m19.5 4.5l2-2m-19 19l2-2"></path><path d="M6.146 11.84c.383-.45 1.09-.454 1.538-.007l4.483 4.484c.447.446.444 1.154-.007 1.537l-1.231 1.047a6.5 6.5 0 0 1-3.133 1.448l-.725.122c-.685.116-1.405-.123-1.918-.637l-.987-.986c-.514-.514-.753-1.234-.637-1.919l.122-.725a6.5 6.5 0 0 1 1.448-3.133z"></path><path stroke-linecap="round" stroke-linejoin="round" d="m8.5 12.5l2-2m1 5l2-2"></path></g></svg>`
	iconActivity    = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:#9CA3AF;flex:none"><path fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M22 12h-4l-3 9L9 3l-3 9H2"></path></svg>`
)

// buildNav assembles the sidebar for the given active item. Dashboard,
// Projects, Integrations (-> /settings/connections, task 4.5), and
// Activity (-> /activity, task 4.5) have real destinations; the rest of
// the mockup's sidebar (Security, Code quality, Errors, Performance, Git
// hygiene) map to future per-category rollup views, not yet built — they
// render present-but-disabled rather than as dead links, and their counts
// are intentionally blank rather than fabricated.
func buildNav(active string, projectCount int) *httpx.Nav {
	return &httpx.Nav{
		Active: active,
		Items: []httpx.NavItem{
			// Projects is the landing page until there is a dashboard worth
			// the name; the old "Dashboard" entry pointed at this same list
			// under a different label, which only invited the question of
			// what the difference was.
			{Key: "projects", Label: "Projects", Href: "/projects", Count: fmt.Sprintf("%d", projectCount), Icon: template.HTML(iconProjects)}, //nolint:gosec // constant, not user input
			{Key: "security", Label: "Security", Icon: template.HTML(iconSecurity)},
			{Key: "code_quality", Label: "Code quality", Icon: template.HTML(iconCodeQuality)},
			{Key: "errors", Label: "Errors", Icon: template.HTML(iconErrors)},
			{Key: "performance", Label: "Performance", Icon: template.HTML(iconPerformance)},
			{Key: "git_hygiene", Label: "Git hygiene", Icon: template.HTML(iconGitHygiene)},
			{Key: "qa_tools", Label: "QA Tools", Icon: template.HTML(iconQATools)}, //nolint:gosec // constant, not user input
			{Key: "integrations", Label: "Integrations", Href: "/settings/connections", Icon: template.HTML(iconIntegration)},
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

// projectTrail is the breadcrumb path for a project page: the project
// itself, as the current page.
func projectTrail(project *repo.Project) []httpx.NavCrumb {
	return []httpx.NavCrumb{{Label: project.Name}}
}

// componentTrail is the path for anything at or below a component —
// project → component → whatever the caller adds. Callers pass the deeper
// segments rather than each rebuilding the first two, so a change to how
// projects are linked lands in one place.
func componentTrail(project *repo.Project, component *repo.Component, deeper ...httpx.NavCrumb) []httpx.NavCrumb {
	trail := []httpx.NavCrumb{
		{Label: project.Name, Href: "/projects/" + project.ID.String()},
	}
	if len(deeper) == 0 {
		trail = append(trail, httpx.NavCrumb{Label: component.Name})
		return trail
	}
	trail = append(trail, httpx.NavCrumb{Label: component.Name, Href: "/components/" + component.ID.String()})
	return append(trail, deeper...)
}

// iconQATools: a clipboard with a check — test plans and runs.
const iconQATools = `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" style="color:currentColor;flex:none"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"><path d="M9 4H7.5A2.5 2.5 0 0 0 5 6.5v12A2.5 2.5 0 0 0 7.5 21h9a2.5 2.5 0 0 0 2.5-2.5v-12A2.5 2.5 0 0 0 16.5 4H15"></path><rect x="9" y="2.5" width="6" height="3" rx="1"></rect><path d="m9.5 13.5l1.75 1.75L14.5 12"></path></g></svg>`
