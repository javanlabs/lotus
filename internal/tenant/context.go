package tenant

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/repo/global"
)

// localsKey is an unexported type so these Locals keys can never collide
// with a string key set by other middleware.
type localsKey int

const (
	localsResolvedKey localsKey = iota
	localsScopeKey
)

// OrgFromContext returns the organization the request resolved to, if any.
// Only populated when RequireTenant (or a check of Resolved.Kind) confirms
// the Host was a tenant subdomain.
func OrgFromContext(c *fiber.Ctx) (*global.Org, bool) {
	resolved, ok := c.Locals(localsResolvedKey).(Resolved)
	if !ok || resolved.Kind != HostTenant {
		return nil, false
	}
	return resolved.Org, true
}

// FromContext is the only way a handler can obtain a Scope: it reads back
// what Resolver.Middleware already constructed via the unexported
// newScope, for the org the request's Host resolved to. There is no
// exported constructor a handler could call instead.
func FromContext(c *fiber.Ctx) (Scope, bool) {
	s, ok := c.Locals(localsScopeKey).(Scope)
	return s, ok
}
