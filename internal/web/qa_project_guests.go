package web

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// Guests of a project (project_member.manage): the organization's guests
// listed on it see its QA, nothing else. Invitations name the projects
// (Settings -> Members). The project page's panel for these endpoints was
// removed on 2026-10-04; the endpoints stay for whichever screen takes
// guest listings over.

func auditProjectMember(c *fiber.Ctx, s tenant.Scope, action string, projectID, userID uuid.UUID) error {
	raw, _ := json.Marshal(fiber.Map{"project_id": projectID, "user_id": userID})
	return repo.RecordAuditEvent(c.Context(), s, qaActor(c), action, "project", &projectID, raw)
}

// guestOf reads a user id that must be a guest of this organization.
func guestOf(c *fiber.Ctx, d Deps, s tenant.Scope, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fiber.ErrNotFound
	}
	m, err := global.ActiveMembership(c.Context(), d.DB, id, s.OrgID())
	if err != nil || m == nil || m.RoleKey != "guest" {
		return uuid.Nil, fiber.ErrNotFound
	}
	return id, nil
}

func addProjectGuestUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		userID, err := guestOf(c, d, s, c.FormValue("user_id"))
		if err != nil {
			return err
		}
		if err := repo.AddProjectMember(c.Context(), s, project.ID, userID); err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return fiber.ErrNotFound
			}
			return writeRepoErr(c, err)
		}
		if err := auditProjectMember(c, s, "project_member.add", project.ID, userID); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "project.guest.added")
		return redirectAfterMutation(c, projectURL(project))
	}
}

func removeProjectGuestUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		userID, err := uuid.Parse(c.Params("uid"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.RemoveProjectMember(c.Context(), s, project.ID, userID); err != nil {
			return writeRepoErr(c, err)
		}
		if err := auditProjectMember(c, s, "project_member.remove", project.ID, userID); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "project.guest.removed")
		return redirectAfterMutation(c, projectURL(project))
	}
}

func registerProjectGuestRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post("/projects/:id/guests", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project_member.manage"), addProjectGuestUI(d))
	app.Post("/projects/:id/guests/:uid/remove", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("project_member.manage"), removeProjectGuestUI(d))
}
