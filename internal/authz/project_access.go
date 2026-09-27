package authz

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ProjectMemberCheck reports whether userID is listed on projectID. The web
// layer passes a closure over repo.IsProjectMember; authz does not import
// repo.
type ProjectMemberCheck func(c *fiber.Ctx, projectID, userID uuid.UUID) (bool, error)

// IsProjectScoped reports whether p only reaches the projects it is listed
// on. Today that is exactly the guest role.
func IsProjectScoped(p Principal) bool { return p.Role == "guest" }

// RequireProjectAccess guards every /projects/:id/qa… route. Everyone but a
// guest passes (the Require(perm) guard next to it decides what they may
// do). A guest passes only for a project it is listed on; anything else is
// 404, never 403, so a guest cannot learn which project ids exist.
func RequireProjectAccess(check ProjectMemberCheck, param string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		p, ok := CurrentPrincipal(c)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "no principal")
		}
		if !IsProjectScoped(p) {
			return c.Next()
		}
		projectID, err := uuid.Parse(c.Params(param))
		if err != nil {
			return fiber.ErrNotFound
		}
		member, err := check(c, projectID, p.UserID)
		if err != nil {
			return err
		}
		if !member {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}
