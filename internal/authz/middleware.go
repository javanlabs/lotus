package authz

import (
	"reflect"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/reqctx"
	"github.com/uyab/lotus/internal/tenant"
)

type localsKey int

const localsPrincipalKey localsKey = iota

// CurrentPrincipal returns the Principal LoadPrincipal (or, for a Bearer
// token request, authn.RequireTokenOrSession) resolved for this request,
// if any.
func CurrentPrincipal(c *fiber.Ctx) (Principal, bool) {
	p, ok := c.Locals(localsPrincipalKey).(Principal)
	return p, ok
}

// SetPrincipal stores a Principal resolved by a path other than
// LoadPrincipal — task 1.8's Bearer-token auth, specifically. Exported so
// internal/authn can compose with LoadPrincipal in a single route chain
// without authz importing authn (see internal/reqctx's doc comment for why
// that import would cycle).
func SetPrincipal(c *fiber.Ctx, p Principal) {
	c.Locals(localsPrincipalKey, p)
}

// LoadPrincipal builds a session-based Principal from the session
// authn.RequireTenantSession already validated (stored via internal/reqctx
// — see that package's doc comment for why authz reads it from there
// rather than importing authn directly). Must be mounted after
// RequireTenantSession — it has no way to establish a session itself, only
// to turn an already-valid one into a Principal. A no-op if a Principal is
// already set — task 1.8's Bearer-token path resolves its own Principal
// upstream of this handler and skips straight past it.
func LoadPrincipal(db *sqlx.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if _, ok := CurrentPrincipal(c); ok {
			return c.Next()
		}
		session, ok := reqctx.CurrentSession(c)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "no session")
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		membership, err := global.ActiveMembership(c.Context(), db, session.UserID, org.ID)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "not a member of this organization")
		}
		c.Locals(localsPrincipalKey, Principal{
			Kind:   KindSession,
			UserID: session.UserID,
			OrgID:  org.ID,
			Role:   membership.RoleKey,
		})
		return c.Next()
	}
}

// Require is the declarative route guard from task 1.4: greppable, visible
// in diffs, and the thing task 1.5's route-coverage test checks every
// route has one of (this or authz.Public). A principal that exists but
// lacks perm gets 403 — they're a known, authenticated member of this org,
// just not allowed to do this. That's a different failure than the
// object-level "does this resource even belong to your org" check (see
// CheckOwnership), which is 404 specifically to avoid confirming a
// cross-org object's existence to someone who can't see it.
func Require(perm string) fiber.Handler {
	h := func(c *fiber.Ctx) error {
		p, ok := CurrentPrincipal(c)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "no principal")
		}
		if !p.Can(perm) {
			return fiber.NewError(fiber.StatusForbidden, "missing permission: "+perm)
		}
		return c.Next()
	}
	guardMu.Lock()
	requirePtrs[reflect.ValueOf(h).Pointer()] = true
	guardMu.Unlock()
	return h
}

// Public marks a route as deliberately reachable without an authz
// decision (task 1.5) — health checks, static assets, the OAuth/handoff
// endpoints that exist specifically to establish a session in the first
// place, and so on. reason is mandatory and shows up in the diff and in
// the coverage test's own log output, so marking something public is a
// visible decision, not an accidental omission.
func Public(reason string) fiber.Handler {
	if reason == "" {
		panic("authz.Public: reason must not be empty")
	}
	h := func(c *fiber.Ctx) error { return c.Next() }
	guardMu.Lock()
	publicPtrs[reflect.ValueOf(h).Pointer()] = true
	guardMu.Unlock()
	return h
}

// CheckOwnership is the pattern every handler that loads a resource by a
// URL-param id must follow (task 1.4): compare the loaded row's own org_id
// against the Principal's, and if they don't match, respond 404 — never
// 403, which would confirm to an attacker that the id exists at all,
// turning a permission boundary into an enumeration oracle.
func CheckOwnership(resourceOrgID, principalOrgID uuid.UUID) error {
	if resourceOrgID != principalOrgID {
		return fiber.ErrNotFound
	}
	return nil
}
