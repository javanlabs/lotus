package authn

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/reqctx"
	"github.com/uyab/lotus/internal/tenant"
)

// Session lifetimes (task 1.3). Idle windows are enforced by
// global.SessionByID (last_seen_at must be more recent than
// now()-idleWindow); absolute windows are the hard cap stored as
// sessions.expires_at at creation time.
const (
	globalSessionIdleTTL     = 14 * 24 * time.Hour
	globalSessionAbsoluteTTL = 90 * 24 * time.Hour
	tenantSessionIdleTTL     = 12 * time.Hour
	tenantSessionAbsoluteTTL = 7 * 24 * time.Hour
)

// CurrentSession returns the session RequireTenantSession/RequireGlobal
// Session resolved for this request, if any. A thin re-export of
// reqctx.CurrentSession — see that package's doc comment for why the
// storage itself lives outside both authn and authz.
func CurrentSession(c *fiber.Ctx) (*global.Session, bool) {
	return reqctx.CurrentSession(c)
}

// RequireTenantSession resolves the tenant session cookie, enforces its
// idle/absolute lifetime, and — the core of task 1.3 — compares the
// session's perm_epoch against the organization's current one on every
// request:
//
//   - equal: the session is current, just touch last_seen_at and proceed.
//   - stale, membership still active: rotate — mint a fresh session id
//     carrying the new perm_epoch, revoke the old one, and proceed. A role
//     change takes effect on this very request, no re-login required.
//   - stale, membership gone or suspended: revoke the session and treat
//     the request as unauthenticated. Removing someone from an org hard
//     -rejects their still-alive session instead of leaving it valid until
//     it happens to expire.
//
// On any rejection, GET requests are sent through the handoff flow again
// (RequireTenantSession itself doesn't know how to establish a session —
// only how to validate one); non-GET/htmx requests get a plain 401 so a
// background action fails loudly instead of silently redirecting.
func RequireTenantSession(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if authHandled(c) {
			// task 1.8's RequireTokenOrSession already resolved a Bearer
			// token Principal for this request — there is no cookie to
			// validate, and there never will be one.
			return c.Next()
		}
		resolvedOrg, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}

		cookieVal := c.Cookies(cookieName(tenantSessionCookieBase, deps.CookieSecure))
		if cookieVal == "" {
			return unauthenticated(c)
		}
		session, err := global.SessionByID(c.Context(), deps.DB, sha256Hex(cookieVal), time.Now().Add(-tenantSessionIdleTTL))
		if err != nil || session.OrgID == nil || *session.OrgID != resolvedOrg.ID {
			return unauthenticated(c)
		}

		// The resolver's org cache (task 0.5) can be up to 30s stale, which
		// isn't good enough for a check whose whole point is "the very next
		// request sees the change" — fetch perm_epoch fresh, not cached.
		org, err := global.OrgByID(c.Context(), deps.DB, resolvedOrg.ID)
		if err != nil {
			return unauthenticated(c)
		}

		if session.PermEpoch != org.PermEpoch {
			if _, err := global.ActiveMembership(c.Context(), deps.DB, session.UserID, org.ID); err != nil {
				_ = global.RevokeSession(c.Context(), deps.DB, session.ID)
				c.ClearCookie(cookieName(tenantSessionCookieBase, deps.CookieSecure))
				return unauthenticated(c)
			}

			raw := randToken(32)
			next := global.NewSession{
				ID:        sha256Hex(raw),
				UserID:    session.UserID,
				OrgID:     &org.ID,
				Host:      c.Hostname(),
				PermEpoch: org.PermEpoch,
				ExpiresAt: time.Now().Add(tenantSessionAbsoluteTTL),
			}
			if err := global.RotateSession(c.Context(), deps.DB, session.ID, next); err != nil {
				return err
			}
			c.Cookie(&fiber.Cookie{
				Name:     cookieName(tenantSessionCookieBase, deps.CookieSecure),
				Value:    raw,
				MaxAge:   int(tenantSessionAbsoluteTTL.Seconds()),
				HTTPOnly: true,
				Secure:   deps.CookieSecure,
				SameSite: fiber.CookieSameSiteLaxMode,
				Path:     "/",
			})
			session.ID = sha256Hex(raw)
			session.PermEpoch = org.PermEpoch
		} else {
			_ = global.TouchLastSeen(c.Context(), deps.DB, session.ID)
		}

		reqctx.SetSession(c, session)
		return c.Next()
	}
}

// RequireGlobalSession resolves the auth-host session cookie (task 1.6's
// /welcome and /onboarding/* screens run on the auth host, before any
// organization exists yet, so they need the global session, not a tenant
// one). GET requests without a valid session are sent to /signup; other
// methods get 401.
func RequireGlobalSession(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		session, ok := currentGlobalSession(c, deps.DB, deps.CookieSecure)
		if !ok {
			if c.Method() == fiber.MethodGet {
				return c.Redirect("/signup", fiber.StatusFound)
			}
			return fiber.NewError(fiber.StatusUnauthorized, "no session")
		}
		_ = global.TouchLastSeen(c.Context(), deps.DB, session.ID)
		reqctx.SetSession(c, session)
		return c.Next()
	}
}

func unauthenticated(c *fiber.Ctx) error {
	if c.Method() == fiber.MethodGet && c.Get("HX-Request") == "" {
		return c.Redirect("/handoff/start?return="+c.OriginalURL(), fiber.StatusFound)
	}
	return fiber.NewError(fiber.StatusUnauthorized, "session expired")
}

// Logout revokes the current session (global or tenant, whichever cookie
// is present on this host) and clears its cookie. Purely local: it never
// contacts an identity provider — see task 1.3's note on RP-initiated
// logout, deferred to task 4.5 once per-org SSO exists to have one to
// contact in the first place.
func Logout(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantCookieName := cookieName(tenantSessionCookieBase, deps.CookieSecure)
		authCookieName := cookieName(authSessionCookieBase, deps.CookieSecure)

		if raw := c.Cookies(tenantCookieName); raw != "" {
			_ = global.RevokeSession(c.Context(), deps.DB, sha256Hex(raw))
			c.ClearCookie(tenantCookieName)
		}
		if raw := c.Cookies(authCookieName); raw != "" {
			_ = global.RevokeSession(c.Context(), deps.DB, sha256Hex(raw))
			c.ClearCookie(authCookieName)
		}
		return c.Redirect("/", fiber.StatusFound)
	}
}
