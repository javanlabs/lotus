package web

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// projectPage is GET /projects/:id — a project's own dashboard, reusing
// the exact same "generalized dashboard" view-model and template block as
// the org-wide one (task 4.2's own point), scoped to just this project's
// components via repo.ListComponentMetricsForProject instead of ...ForOrg.
func projectPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		project, err := repo.ProjectByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
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
			components = append(components, componentInfo{ID: comp.ID, Name: comp.Name, ProjectName: project.Name})
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

		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		folderValue := ""
		if project.FolderID != nil {
			folderValue = project.FolderID.String()
		}

		c.Type("html")
		return renderPage(c, d, "pages/project.html", httpx.ViewData{
			Title: project.Name, Org: org, User: user, Nav: navWithTrail("projects", len(projects), projectTrail(folders, project)),
			Data: struct {
				Project      repo.Project
				Dashboard    dashboardViewModel
				CanMove      bool
				CanQA        bool
				FolderSelect []httpx.SelectOption
				FolderValue  string
			}{
				Project: *project, Dashboard: vm,
				CanMove:      mustPrincipal(c).Can("project.update"),
				CanQA:        mustPrincipal(c).Can("qa.read"),
				FolderSelect: folderSelect(folderOptionsFor(folders), nil),
				FolderValue:  folderValue,
			},
		})
	}
}

var componentKinds = []string{"backend", "frontend", "mobile", "service", "library"}

type newComponentFormData struct {
	ProjectID                  uuid.UUID
	ProjectName                string
	Slug, Name, Kind, Language string
	Error                      string
	Kinds                      []string
}

func newComponentPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		projectID, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		project, err := repo.ProjectByID(c.Context(), s, projectID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		return renderNewComponentForm(c, d, newComponentFormData{
			ProjectID: project.ID, ProjectName: project.Name, Kinds: componentKinds,
		}, fiber.StatusOK)
	}
}

func submitNewComponent(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		projectID, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		project, err := repo.ProjectByID(c.Context(), s, projectID)
		if err != nil {
			return writeRepoErr(c, err)
		}

		form := newComponentFormData{
			ProjectID: project.ID, ProjectName: project.Name, Kinds: componentKinds,
			Name:     strings.TrimSpace(c.FormValue("name")),
			Slug:     strings.TrimSpace(c.FormValue("slug")),
			Kind:     strings.TrimSpace(c.FormValue("kind")),
			Language: strings.TrimSpace(c.FormValue("language")),
		}
		if form.Name == "" || form.Slug == "" || form.Kind == "" {
			form.Error = "name, slug, and kind are required"
			return renderNewComponentForm(c, d, form, fiber.StatusUnprocessableEntity)
		}

		comp, err := repo.CreateComponent(c.Context(), s, repo.NewComponent{
			ProjectID: project.ID, Slug: form.Slug, Name: form.Name, Kind: form.Kind, Language: form.Language,
		})
		if err != nil {
			form.Error = componentCreateErrorMessage(err)
			return renderNewComponentForm(c, d, form, fiber.StatusUnprocessableEntity)
		}
		return c.Redirect("/components/"+comp.ID.String(), fiber.StatusFound)
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

func registerProjectPageDetailRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/projects/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), projectPage(d))
	app.Get("/projects/:id/components/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.create"), newComponentPage(d))
	app.Post("/projects/:id/components/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.create"), submitNewComponent(d))
}
