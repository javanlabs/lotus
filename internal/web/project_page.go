package web

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// projectPage is GET /projects/:id (a slug, or an id that canonicalURL
// has already redirected) — a project's own dashboard, reusing
// the exact same "generalized dashboard" view-model and template block as
// the org-wide one (task 4.2's own point), scoped to just this project's
// components via repo.ListComponentMetricsForProject instead of ...ForOrg.
func projectPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
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

		comps, err := repo.ListComponents(c.Context(), s, project.ID, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		components := make([]componentInfo, 0, len(comps))
		for _, comp := range comps {
			components = append(components, componentInfo{ID: comp.ID, Name: comp.Name, Slug: comp.Slug,
				ProjectID: project.ID, ProjectName: project.Name, ProjectSlug: project.Slug})
		}
		metricRows, err := repo.ListComponentMetricsForProject(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		metrics := make(map[uuid.UUID]repo.ComponentMetric, len(metricRows))
		for _, m := range metricRows {
			metrics[m.ComponentID] = m
		}

		subtitle := fmt.Sprintf("%d component%s in this project", len(components), plural(len(components), "", "s"))
		vm := buildDashboardViewModel(subtitle, len(projects), components, metrics)

		tags, err := loadProjectTags(c, s, project)
		if err != nil {
			return writeRepoErr(c, err)
		}

		c.Type("html")
		return renderPage(c, d, "pages/project.html", httpx.ViewData{
			Title: project.Name, Org: org, User: user, Nav: navWithTrail("projects", len(projects), projectTrail(project, trailMenus{Projects: projectMenu(projects, project, "")})),
			Data: struct {
				Project   repo.Project
				URL       string // the project's own page; its links hang off it
				Dashboard dashboardViewModel
				CanQA     bool
				CanDelete bool
				// The project's tags, every tag of the organization (the add
				// box's suggestions), and whether this person may change them.
				Tags, AllTags []repo.Tag
				CanTag        bool
				// The public status page switch, for owners and admins.
				StatusPage statusPageData
			}{
				Project: *project, URL: projectURL(project), Dashboard: vm,
				CanQA:     d.Cfg.Features.QA && mustPrincipal(c).Can("qa.read"),
				CanDelete: mustPrincipal(c).Can("project.delete"),
				Tags:      tags.Tags, AllTags: tags.AllTags, CanTag: tags.CanTag,
				StatusPage: statusPageFor(c, d, project),
			},
		})
	}
}

var componentKinds = []string{"backend", "frontend", "mobile", "service", "library"}

type newComponentFormData struct {
	ProjectURL                 string
	ProjectName                string
	Slug, Name, Kind, Language string
	Error                      string
	Kinds                      []string
}

func newComponentPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		project, err := resolveProject(c)
		if err != nil {
			return err
		}
		return renderNewComponentForm(c, d, newComponentFormData{
			ProjectURL: projectURL(project), ProjectName: project.Name, Kinds: componentKinds,
		}, fiber.StatusOK)
	}
}

func submitNewComponent(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return err
		}

		form := newComponentFormData{
			ProjectURL: projectURL(project), ProjectName: project.Name, Kinds: componentKinds,
			Name:     strings.TrimSpace(c.FormValue("name")),
			Slug:     strings.TrimSpace(c.FormValue("slug")),
			Kind:     strings.TrimSpace(c.FormValue("kind")),
			Language: strings.TrimSpace(c.FormValue("language")),
		}
		if form.Name == "" || form.Slug == "" || form.Kind == "" {
			form.Error = "name, slug, and kind are required"
			return renderNewComponentForm(c, d, form, fiber.StatusUnprocessableEntity)
		}
		if msg := slugRefusal(form.Slug, reservedComponentSlugs); msg != "" {
			form.Error = msg
			return renderNewComponentForm(c, d, form, fiber.StatusUnprocessableEntity)
		}

		comp, err := repo.CreateComponent(c.Context(), s, repo.NewComponent{
			ProjectID: project.ID, Slug: form.Slug, Name: form.Name, Kind: form.Kind, Language: form.Language,
		})
		if err != nil {
			form.Error = componentCreateErrorMessage(err)
			return renderNewComponentForm(c, d, form, fiber.StatusUnprocessableEntity)
		}
		return c.Redirect(componentURL(project, comp), fiber.StatusFound)
	}
}

func componentCreateErrorMessage(err error) string {
	status, msg := repoErrStatus(err)
	if status == fiber.StatusConflict {
		return "a component with that slug already exists in this project"
	}
	return msg
}

func renderNewComponentForm(c *fiber.Ctx, d Deps, form newComponentFormData, status int) error {
	org, user, err := shellChrome(c, d.DB)
	if err != nil {
		return err
	}
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return writeRepoErr(c, err)
	}
	c.Type("html")
	c.Status(status)
	return renderPage(c, d, "pages/component_new.html", httpx.ViewData{
		Title: "New component", Org: org, User: user, Nav: buildNav("projects", len(projects)),
		Data: form,
	})
}

// registerProjectPageDetailRoutes runs after /projects/new is registered
// (registerProjectPageRoutes), and registers /components/new ahead of the
// component page (registerComponentPageRoutes): Fiber takes the first
// match, so the fixed routes win over a slug that spells them — which is
// why those slugs are refused (reservedProjectSlugs).
func registerProjectPageDetailRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/projects/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), projectPage(d))
	app.Get("/projects/:id/components/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.create"), canonicalURL(), newComponentPage(d))
	app.Post("/projects/:id/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.delete"), deleteProjectUI(d))
	app.Post("/projects/:id/components/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.create"), submitNewComponent(d))
}

// deleteProjectUI soft-deletes a project from its own page, once its name has
// been typed. Its history stays in the database.
func deleteProjectUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if strings.TrimSpace(c.FormValue("confirm_name")) != project.Name {
			return formError(c, d, fiber.StatusUnprocessableEntity, "Type the project's name exactly to delete it.")
		}
		if err := repo.SoftDeleteProject(c.Context(), s, project.ID); err != nil {
			return writeRepoErr(c, err)
		}
		meta, _ := json.Marshal(fiber.Map{"name": project.Name, "slug": project.Slug})
		if err := repo.RecordAuditEvent(c.Context(), s, auditActor(c), "project.delete", "project", &project.ID, meta); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "project.deleted", project.Name)
		return redirectAwayAfterMutation(c, "/projects")
	}
}
