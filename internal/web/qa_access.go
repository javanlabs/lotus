package web

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Every /projects/:id/qa… route runs qaRoute's chain: the tenant and session
// guards, then RequireProjectAccess (a guest reaches only the projects it is
// listed on, and gets 404 for any other), then the route's permission.
//
// RLS pins every query to the organization, not to the project, and the
// repo's by-id functions look rows up by id alone. So a handler that takes
// any other id from the URL or the form — section, case, scenario, group —
// checks it belongs to :id with qaOwned before using it, and answers 404
// when it does not.

func projectMemberCheck() authz.ProjectMemberCheck {
	return func(c *fiber.Ctx, ref string, userID uuid.UUID) (bool, error) {
		s, err := mustScope(c)
		if err != nil {
			return false, err
		}
		project, err := lookupProject(c, ref)
		if errors.Is(err, fiber.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return repo.IsProjectMember(c.Context(), s, project.ID, userID)
	}
}

// qaRoute returns the middleware chain for a /projects/:id/qa… route
// followed by the handler. A page fetched by project id is sent to its
// slug URL once the guards have passed (canonicalURL).
func qaRoute(requireSession, loadPrincipal fiber.Handler, perm string, h fiber.Handler) []fiber.Handler {
	return []fiber.Handler{
		tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.RequireProjectAccess(projectMemberCheck(), projectParam), authz.Require(perm), canonicalURL(), h,
	}
}

// loadQAProject reads :id, a project's slug or id. An unknown project is
// 404.
func loadQAProject(c *fiber.Ctx) (tenant.Scope, *repo.Project, error) {
	s, err := mustScope(c)
	if err != nil {
		return s, nil, err
	}
	p, err := resolveProject(c)
	return s, p, err
}

// qaOwned checks that a row named in the request belongs to the project in
// the URL. Anything else — another project's row, an unknown id, a
// malformed one — is 404.
func qaOwned(c *fiber.Ctx, s tenant.Scope, project *repo.Project, kind repo.QAKind, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fiber.ErrNotFound
	}
	owner, err := repo.QAProjectOf(c.Context(), s, kind, id)
	if errors.Is(err, repo.ErrNotFound) || (err == nil && owner != project.ID) {
		return uuid.Nil, fiber.ErrNotFound
	}
	return id, err
}

// qaNav is the sidebar and breadcrumb for a project's QA page. Members
// go through the project page; a guest has no
// dashboard.read, so its path runs QA Tools → project instead. deeper are
// the segments below the QA landing; the last segment is the current page
// and is never a link.
func qaNav(c *fiber.Ctx, s tenant.Scope, project *repo.Project, deeper ...httpx.NavCrumb) (*httpx.Nav, error) {
	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return nil, err
	}
	var trail []httpx.NavCrumb
	if authz.IsProjectScoped(mustPrincipal(c)) {
		trail = []httpx.NavCrumb{{Label: "QA Tools", Href: "/qa"}, {Label: project.Name, Href: qaBase(project)}}
	} else {
		trail = []httpx.NavCrumb{{Label: project.Name, Href: projectURL(project)}, {Label: "QA", Href: qaBase(project)}}
	}
	trail = append(trail, deeper...)
	trail[len(trail)-1].Href = ""
	return navWithTrail("qa_tools", len(projects), trail), nil
}

// qaFormError answers a rejected QA form the way formError does: the
// message inside the still-open dialog for htmx, the status otherwise.
func qaFormError(c *fiber.Ctx, d Deps, status int, msg string) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "form_error", msg)
}

func qaActor(c *fiber.Ctx) *uuid.UUID { return auditActor(c) }

// redirectAfterCreate ends a successful create that should open what it
// made. A plain post gets a redirect to it; an htmx post (a dialog) gets
// HX-Redirect, because HX-Refresh — what redirectAfterMutation sends —
// would reload the page the dialog sits on and leave the person there.
func redirectAfterCreate(c *fiber.Ctx, to string) error {
	if c.Get("HX-Request") != "" {
		c.Set("HX-Redirect", to)
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect(to, fiber.StatusFound)
}
