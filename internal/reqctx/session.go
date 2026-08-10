// Package reqctx holds the one piece of per-request state that both
// internal/authn (which establishes it) and internal/authz (which reads it
// to build a Principal) need: the resolved session. It exists purely to
// break the import cycle that would otherwise result — authn depends on
// authz for permission checks in a couple of places (task 1.6's
// onboarding invite screen), and authz depends on authn for session
// lookup, so the shared bit neither owns exclusively lives here instead.
package reqctx

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/repo/global"
)

type localsKey int

const sessionKey localsKey = iota

// SetSession stores the resolved session on the request context.
func SetSession(c *fiber.Ctx, s *global.Session) {
	c.Locals(sessionKey, s)
}

// CurrentSession returns the session set by SetSession, if any.
func CurrentSession(c *fiber.Ctx) (*global.Session, bool) {
	s, ok := c.Locals(sessionKey).(*global.Session)
	return s, ok
}
