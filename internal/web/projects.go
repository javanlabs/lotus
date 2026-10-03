package web

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// projectRoutes and componentRoutes (task 2.1) are JSON handlers only —
// the screens that call them are task 4.3. Every mutation goes through
// repo, which goes through the request's tenant.Scope, so a cross-org id
// in any :id/:project_id param simply resolves to repo.ErrNotFound (RLS
// scopes the row out at the database level) rather than needing an
// explicit ownership check here — that's what gets task 2.1's "cross-org
// access is 404, not 403" acceptance criterion for free.

func mustScope(c *fiber.Ctx) (tenant.Scope, error) {
	s, ok := tenant.FromContext(c)
	if !ok {
		return tenant.Scope{}, fiber.ErrNotFound
	}
	return s, nil
}

func repoErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return fiber.StatusNotFound, "not found"
	case errors.Is(err, repo.ErrSlugTaken):
		return fiber.StatusConflict, "slug already taken in this organization"
	case errors.Is(err, repo.ErrInvalidInput):
		return fiber.StatusBadRequest, "invalid input"
	default:
		return fiber.StatusInternalServerError, "internal error"
	}
}

func writeRepoErr(c *fiber.Ctx, err error) error {
	status, msg := repoErrStatus(err)
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

type createProjectRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func createProject(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	var req createProjectRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Slug == "" || req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "slug and name are required")
	}
	if msg := slugRefusal(req.Slug, reservedProjectSlugs); msg != "" {
		return fiber.NewError(fiber.StatusBadRequest, msg)
	}
	p, err := repo.CreateProject(c.Context(), s, repo.NewProject{Slug: req.Slug, Name: req.Name, Description: req.Description})
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(p)
}

func listProjects(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	includeArchived := c.Query("include_archived") == "true"
	projects, err := repo.ListProjects(c.Context(), s, includeArchived)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(projects)
}

func getProject(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	p, err := repo.ProjectByID(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(p)
}

type updateProjectRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
}

func updateProject(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req updateProjectRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Slug == "" || req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "slug and name are required")
	}
	if msg := slugRefusal(req.Slug, reservedProjectSlugs); msg != "" {
		return fiber.NewError(fiber.StatusBadRequest, msg)
	}
	p, err := repo.UpdateProject(c.Context(), s, id, repo.ProjectUpdate{
		Slug: req.Slug, Name: req.Name, Description: req.Description, SortOrder: req.SortOrder,
	})
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(p)
}

func setProjectArchived(archived bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.SetProjectArchived(c.Context(), s, id, archived); err != nil {
			return writeRepoErr(c, err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	}
}

func projectDeleteImpact(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	impact, err := repo.ProjectDeletionImpactOf(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(impact)
}

// deleteProject requires the caller to echo back the current impact
// counts (as query params) so a hard delete can only proceed against the
// counts the confirmation dialog actually showed — if either count has
// moved since, the request is rejected rather than silently deleting more
// than what was confirmed.
func deleteProject(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	impact, err := repo.ProjectDeletionImpactOf(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	if !confirmedCount(c, "confirm_components", impact.Components) || !confirmedCount(c, "confirm_bindings", impact.Bindings) ||
		!confirmedCount(c, "confirm_qa_cases", impact.QACases) || !confirmedCount(c, "confirm_qa_runs", impact.QARuns) ||
		!confirmedCount(c, "confirm_qa_issues", impact.QAIssues) {
		return fiber.NewError(fiber.StatusConflict, "deletion impact changed; re-confirm before deleting")
	}
	if err := repo.HardDeleteProject(c.Context(), s, id); err != nil {
		return writeRepoErr(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// confirmedCount reports whether the query parameter echoes n. A missing
// parameter confirms 0, so callers written before a count existed keep
// working for projects that have none of it — and are refused for projects
// that do, which is the point.
func confirmedCount(c *fiber.Ctx, param string, n int) bool {
	v := c.Query(param)
	if v == "" {
		v = "0"
	}
	return v == strconv.Itoa(n)
}

type createComponentRequest struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Language string `json:"language"`
}

func createComponent(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	projectID, err := uuid.Parse(c.Params("project_id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req createComponentRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Slug == "" || req.Name == "" || req.Kind == "" {
		return fiber.NewError(fiber.StatusBadRequest, "slug, name, and kind are required")
	}
	if msg := slugRefusal(req.Slug, reservedComponentSlugs); msg != "" {
		return fiber.NewError(fiber.StatusBadRequest, msg)
	}
	comp, err := repo.CreateComponent(c.Context(), s, repo.NewComponent{
		ProjectID: projectID, Slug: req.Slug, Name: req.Name, Kind: req.Kind, Language: req.Language,
	})
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(comp)
}

func listComponents(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	projectID, err := uuid.Parse(c.Params("project_id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	includeArchived := c.Query("include_archived") == "true"
	comps, err := repo.ListComponents(c.Context(), s, projectID, includeArchived)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(comps)
}

func getComponent(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	comp, err := repo.ComponentByID(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(comp)
}

type updateComponentRequest struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Language  string `json:"language"`
	SortOrder int    `json:"sort_order"`
}

func updateComponent(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req updateComponentRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Slug == "" || req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "slug and name are required")
	}
	if msg := slugRefusal(req.Slug, reservedComponentSlugs); msg != "" {
		return fiber.NewError(fiber.StatusBadRequest, msg)
	}
	comp, err := repo.UpdateComponent(c.Context(), s, id, repo.ComponentUpdate{
		Slug: req.Slug, Name: req.Name, Language: req.Language, SortOrder: req.SortOrder,
	})
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(comp)
}

func setComponentArchived(archived bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.SetComponentArchived(c.Context(), s, id, archived); err != nil {
			return writeRepoErr(c, err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	}
}

func componentDeleteImpact(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	impact, err := repo.ComponentDeletionImpactOf(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(impact)
}

func deleteComponent(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	impact, err := repo.ComponentDeletionImpactOf(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	if c.Query("confirm_bindings") != strconv.Itoa(impact.Bindings) {
		return fiber.NewError(fiber.StatusConflict, "deletion impact changed; re-confirm before deleting")
	}
	if err := repo.HardDeleteComponent(c.Context(), s, id); err != nil {
		return writeRepoErr(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// registerProjectRoutes mounts task 2.1's routes. Called from New() after
// the session/principal middleware chain is established, same pattern as
// every Fase 1 tenant route.
func registerProjectRoutes(app *fiber.App, requireSession fiber.Handler, loadPrincipal fiber.Handler) {
	app.Post("/projects", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.create"), createProject)
	app.Get("/projects", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), listProjects)
	app.Get("/projects/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), getProject)
	app.Patch("/projects/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), updateProject)
	app.Post("/projects/:id/archive", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), setProjectArchived(true))
	app.Post("/projects/:id/unarchive", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.update"), setProjectArchived(false))
	app.Get("/projects/:id/delete-impact", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.delete"), projectDeleteImpact)
	app.Delete("/projects/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project.delete"), deleteProject)

	app.Post("/projects/:project_id/components", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.create"), createComponent)
	app.Get("/projects/:project_id/components", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), listComponents)
	app.Get("/components/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), getComponent)
	app.Patch("/components/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.update"), updateComponent)
	app.Post("/components/:id/archive", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.update"), setComponentArchived(true))
	app.Post("/components/:id/unarchive", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.update"), setComponentArchived(false))
	app.Get("/components/:id/delete-impact", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.delete"), componentDeleteImpact)
	app.Delete("/components/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("component.delete"), deleteComponent)
}
