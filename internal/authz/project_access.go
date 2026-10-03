package authz

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ProjectMemberCheck reports whether userID is listed on the project ref
// names. ref is the URL segment as written — the project's id or its slug —
// so resolving it is the check's job: a ref that names no project is
// simply false, never an error. The web layer passes a closure over its
// project resolver and repo.IsProjectMember; authz does not import repo.
type ProjectMemberCheck func(c *fiber.Ctx, ref string, userID uuid.UUID) (bool, error)

// IsProjectScoped reports whether p only reaches the projects it is listed
// on. Today that is exactly the guest role.
func IsProjectScoped(p Principal) bool { return p.Role == "guest" }

// RequireProjectAccess guards every /projects/:id/qa… route. Everyone but a
// guest passes (the Require(perm) guard next to it decides what they may
// do). A guest passes only for a project it is listed on; anything else is
// 404, never 403, so a guest cannot learn which project ids or slugs exist.
func RequireProjectAccess(check ProjectMemberCheck, param string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		p, ok := CurrentPrincipal(c)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "no principal")
		}
		if !IsProjectScoped(p) {
			return c.Next()
		}
		member, err := check(c, c.Params(param), p.UserID)
		if err != nil {
			return err
		}
		if !member {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}
