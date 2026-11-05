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

		all, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		// One flat list narrowed by the tag chips. A ?folder= left in an old
		// bookmark is simply ignored: folders are not part of the UI any more.
		filter, err := loadTagFilter(c, s, "/projects")
		if err != nil {
			return writeRepoErr(c, err)
		}
		keep, err := filter.projectFilter(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		projects := all
		if keep != nil {
			projects = make([]repo.Project, 0, len(keep))
			for _, p := range all {
				if keep[p.ID] {
					projects = append(projects, p)
				}
			}
		}

		var components []componentInfo
		for _, p := range projects {
			comps, err := repo.ListComponents(c.Context(), s, p.ID, false)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, comp := range comps {
				components = append(components, componentInfo{ID: comp.ID, Name: comp.Name, Slug: comp.Slug,
					ProjectID: p.ID, ProjectName: p.Name, ProjectSlug: p.Slug})
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
		tags, err := repo.ProjectTagsByProject(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}

		subtitle := fmt.Sprintf("%d project%s · %d component%s across %s", len(projects), plural(len(projects), "", "s"), len(components), plural(len(components), "", "s"), org.Name)
		vm := buildProjectDashboardViewModel(subtitle, projects, tags, components, metrics)
		vm.Filter = filter
		if len(all) > 0 {
			// A filter that matches nothing still shows the list (and the
			// chips to get out of it), never "No projects yet".
			vm.Empty, vm.NoProjects = false, false
		}

		c.Type("html")
		return renderPage(c, d, "pages/projects.html", httpx.ViewData{
			Title: "Projects", Org: org, User: user,
			Nav:  buildNav("projects", len(all)),
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
	// The project list. Registered here — before registerProjectRoutes'
	// JSON handler for the same path — so the browser gets HTML. See
	// AGENTS.md on the shadowed JSON endpoints.
	app.Get("/projects", tenant.RequireTenant(), requireSession, loadPrincipal,
		guestHome(d), authz.Require("dashboard.read"), dashboardPage(d))

	// The landing page (home_dashboard.go). A guest is sent on to its own
	// work, as it is from the project list.
	app.Get("/dashboard", tenant.RequireTenant(), requireSession, loadPrincipal,
		guestHome(d), authz.Require("dashboard.read"), homeDashboardPage(d))
}
