package web

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// newProjectFormData is what pages/project_new.html renders.
type newProjectFormData struct {
	Slug, Name, Description, Error string
}

func newProjectPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
		return renderNewProjectForm(c, d, org, user, len(projects), newProjectFormData{}, fiber.StatusOK)
	}
}

func submitNewProject(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		form := newProjectFormData{
			Name:        strings.TrimSpace(c.FormValue("name")),
			Slug:        strings.TrimSpace(c.FormValue("slug")),
			Description: strings.TrimSpace(c.FormValue("description")),
		}
		if form.Name == "" || form.Slug == "" {
			form.Error = "name and slug are required"
			return renderNewProjectFormWithCount(c, d, s, org, user, form, fiber.StatusUnprocessableEntity)
		}

		if _, err := repo.CreateProject(c.Context(), s, repo.NewProject{Slug: form.Slug, Name: form.Name, Description: form.Description}); err != nil {
			switch {
			case errors.Is(err, repo.ErrSlugTaken):
				form.Error = "a project with that slug already exists"
			case errors.Is(err, repo.ErrInvalidInput):
				form.Error = "invalid input"
			default:
				return err
			}
			return renderNewProjectFormWithCount(c, d, s, org, user, form, fiber.StatusUnprocessableEntity)
		}

		return c.Redirect("/dashboard", fiber.StatusFound)
	}
}

func renderNewProjectFormWithCount(c *fiber.Ctx, d Deps, s tenant.Scope, org *httpx.Org, user *httpx.User, form newProjectFormData, status int) error {
	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return renderNewProjectForm(c, d, org, user, len(projects), form, status)
}

func renderNewProjectForm(c *fiber.Ctx, d Deps, org *httpx.Org, user *httpx.User, projectCount int, form newProjectFormData, status int) error {
	c.Type("html")
	c.Status(status)
	return d.Renderer.Render(c, "pages/project_new.html", httpx.ViewData{
		Title: "New project", Org: org, User: user, Nav: buildNav("projects", projectCount),
		Data: form,
	})
}

func registerProjectPageRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/projects/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.create"), newProjectPage(d))
	app.Post("/projects/new", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.create"), submitNewProject(d))
}
