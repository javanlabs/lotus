package web

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// dashboardPage is the fleet-wide "generalized dashboard" (tasks 4.1/4.2):
// every component across every project in the organization, rolled up
// into a Lotus Score, four per-kind KPI cards, and a repositories table.
// GIT/SONAR/TRIVY/SENTRY badges are real collector kinds; the mockup's K6
// and ZAP columns aren't (no such collector exists yet — see the mockup's
// own "New project" view, which marks them PENDING) and are omitted here
// rather than faked.
func dashboardPage(d Deps) fiber.Handler {
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

		var components []componentInfo
		for _, p := range projects {
			comps, err := repo.ListComponents(c.Context(), s, p.ID, false)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, comp := range comps {
				components = append(components, componentInfo{ID: comp.ID, Name: comp.Name, ProjectID: p.ID, ProjectName: p.Name})
			}
		}

		metricRows, err := repo.ListComponentMetricsForOrg(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		metrics := make(map[uuid.UUID]repo.ComponentMetric, len(metricRows))
		for _, m := range metricRows {
			metrics[m.ComponentID] = m
		}

		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}

		// ?folder=<id> opens one folder of the folder view. A malformed id,
		// or one this organization cannot see (RLS already hid it from
		// ListFolders), is a 404 like any other missing resource.
		var current *uuid.UUID
		if q := c.Query("folder"); q != "" {
			id, err := uuid.Parse(q)
			if err != nil || !hasFolder(folders, id) {
				return fiber.ErrNotFound
			}
			current = &id
		}

		subtitle := fmt.Sprintf("%d project%s · %d component%s across %s", len(projects), plural(len(projects), "", "s"), len(components), plural(len(components), "", "s"), org.Name)
		vm := buildProjectDashboardViewModel(subtitle, projects, folders, components, metrics, current)
		p := mustPrincipal(c)
		vm.CanCreateFolder = p.Can("folder.create")
		vm.CanEditFolder = p.Can("folder.update")
		vm.CanDeleteFolder = p.Can("folder.delete")
		vm.CanMoveProjects = p.Can("project.update")

		c.Type("html")
		return renderPage(c, d, "pages/projects.html", httpx.ViewData{
			Title: "Projects", Org: org, User: user,
			Nav:  folderNav(len(projects), vm),
			Data: vm,
		})
	}
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func registerDashboardRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	// /projects, not /dashboard: the list of projects IS the landing page
	// for now. Registered here — before registerProjectRoutes' JSON
	// handler for the same path — so the browser gets HTML. See AGENTS.md
	// on the shadowed JSON endpoints.
	app.Get("/projects", tenant.RequireTenant(), requireSession, loadPrincipal,
		guestHome, authz.Require("dashboard.read"), dashboardPage(d))

	// Kept so links and bookmarks made while this lived at /dashboard keep
	// working. Public: it only forwards, and the page it forwards to
	// enforces its own session.
	app.Get("/dashboard", tenant.RequireTenant(),
		authz.Public("permanent forward to /projects; the target enforces the session"),
		func(c *fiber.Ctx) error { return c.Redirect("/projects", fiber.StatusMovedPermanently) })
}

func hasFolder(folders []repo.Folder, id uuid.UUID) bool {
	for _, f := range folders {
		if f.ID == id {
			return true
		}
	}
	return false
}

// folderNav is the folder view's navigation: the open folder's path as the
// breadcrumb, with the organization's name before it — linking back to the
// top level and, for anyone who may move things, accepting drops there.
func folderNav(projectCount int, vm dashboardViewModel) *httpx.Nav {
	canDrop := vm.CanMoveProjects || vm.CanEditFolder
	nav := navWithTrail("projects", projectCount, folderTrail(vm.Crumbs, canDrop))
	nav.OrgDrop = canDrop && len(nav.Trail) > 0
	return nav
}
