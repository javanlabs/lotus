package authn

import (
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"
)

const stateCookieTTL = 600 * time.Second

// Registry holds every configured provider by name ("github", "google").
type Registry map[string]Provider

// Login starts an OAuth flow: GET /login/:provider[?return=/path]. Only
// reachable on the auth host — see tenant.RequireAuth in how this is
// mounted.
func Login(deps Deps) fiber.Handler {
	db, providers, cookieSecure := deps.DB, deps.OAuthProviders, deps.CookieSecure
	return func(c *fiber.Ctx) error {
		p, ok := providers[c.Params("provider")]
		if !ok {
			return fiber.ErrNotFound
		}

		state := randToken(32)
		verifier, challenge := newPKCE()
		returnPath := sanitizeReturnPath(c.Query("return"))

		err := global.CreateOAuthState(c.Context(), db, global.NewOAuthState{
			StateHash:    sha256Hex(state),
			Provider:     p.Name(),
			CodeVerifier: verifier,
			ReturnPath:   returnPath,
			ExpiresAt:    time.Now().Add(stateCookieTTL),
		})
		if err != nil {
			return err
		}

		c.Cookie(&fiber.Cookie{
			Name:     cookieName(stateCookieBase, cookieSecure),
			Value:    state,
			MaxAge:   int(stateCookieTTL.Seconds()),
			HTTPOnly: true,
			Secure:   cookieSecure,
			SameSite: fiber.CookieSameSiteLaxMode,
			Path:     "/",
		})
		authURL, err := p.AuthCodeURL(state, challenge)
		if err != nil {
			return err
		}
		return c.Redirect(authURL, fiber.StatusFound)
	}
}

// Callback completes an OAuth flow: GET /login/:provider/callback.
func Callback(deps Deps) fiber.Handler {
	db, providers, cookieSecure := deps.DB, deps.OAuthProviders, deps.CookieSecure
	return func(c *fiber.Ctx) error {
		p, ok := providers[c.Params("provider")]
		if !ok {
			return fiber.ErrNotFound
		}

		stateName := cookieName(stateCookieBase, cookieSecure)
		cookieState := c.Cookies(stateName)
		stateParam := c.Query("state")
		c.ClearCookie(stateName)

		// Every way this can fail short of a database fault sends the person
		// back to sign in again with a sentence saying why. A bare 400 is
		// the whole page as far as the browser is concerned, and it left
		// someone who had simply taken too long on the Keycloak page staring
		// at "invalid or missing oauth state" with no way forward.
		//
		// None of these redirects weakens a check: no session is created on
		// any of them, and the state is still single-use and cookie-bound.

		// The IdP reports a refusal (the person clicked Cancel, or the
		// client is misconfigured) by coming back with ?error= and no code.
		if idpErr := c.Query("error"); idpErr != "" {
			if idpErr == "access_denied" {
				return redirectToLogin(c, deps, NoticeLoginCancelled, "")
			}
			log.Printf("oauth callback: %s returned error=%q description=%q", p.Name(), idpErr, c.Query("error_description"))
			return redirectToLogin(c, deps, NoticeLoginFailed, "")
		}

		// A missing cookie is by far the common case: the state cookie lives
		// ten minutes, so a sign-in page left open longer comes back without
		// it. A mismatch is either a stale tab or a forged callback; both get
		// the same answer, and neither gets told which it was.
		if stateParam == "" || cookieState == "" || stateParam != cookieState {
			return redirectToLogin(c, deps, NoticeLoginExpired, "")
		}

		consumed, err := global.ConsumeOAuthState(c.Context(), db, sha256Hex(stateParam))
		if err != nil {
			return redirectToLogin(c, deps, NoticeLoginExpired, "")
		}
		if consumed.Provider != p.Name() {
			log.Printf("oauth callback: state issued for %q arrived at %q", consumed.Provider, p.Name())
			return redirectToLogin(c, deps, NoticeLoginFailed, "")
		}

		code := c.Query("code")
		if code == "" {
			return redirectToLogin(c, deps, NoticeLoginFailed, consumed.ReturnPath)
		}
		// Exchange and userinfo failures are the IdP or its configuration
		// (a rotated client secret, an unreachable issuer) — nothing the
		// person can fix, so they get a retry prompt and the operator gets
		// the detail in the log. The token itself is never logged.
		accessToken, err := p.Exchange(c.Context(), code, consumed.CodeVerifier)
		if err != nil {
			log.Printf("oauth callback: %s token exchange: %v", p.Name(), err)
			return redirectToLogin(c, deps, NoticeLoginFailed, consumed.ReturnPath)
		}
		identity, err := p.FetchIdentity(c.Context(), accessToken)
		if err != nil {
			log.Printf("oauth callback: %s identity: %v", p.Name(), err)
			return redirectToLogin(c, deps, NoticeLoginFailed, consumed.ReturnPath)
		}

		user, _, err := ResolveUser(c.Context(), db, p.Name(), identity)
		if err != nil {
			return err
		}
		if err := global.TouchLastLogin(c.Context(), db, user.ID); err != nil {
			return err
		}

		// Single mode only: the first verified login matching the address
		// `lotus init` nominated claims Owner. Until that happens nobody
		// else may sign in, so a fresh instance reachable from the network
		// is never a land-grab. A no-op once ownership is claimed.
		if deps.Cfg.IsSingle() {
			if _, err := ClaimOwnershipIfPending(c.Context(), db, user.ID, identity); err != nil {
				if errors.Is(err, ErrNotTheOwner) {
					return redirectToLogin(c, deps, NoticeOwnerPending, consumed.ReturnPath)
				}
				return err
			}
		}

		if _, err := createGlobalSession(c, db, user.ID, cookieSecure); err != nil {
			return err
		}

		return c.Redirect(consumed.ReturnPath, fiber.StatusFound)
	}
}

// createGlobalSession mints a new global (auth-host) session and sets its
// cookie. Returns the raw cookie value (tests use this to assert a fresh
// session id each time — task 1.3's anti-fixation requirement).
func createGlobalSession(c *fiber.Ctx, db *sqlx.DB, userID uuid.UUID, cookieSecure bool) (string, error) {
	raw := randToken(32)
	var ip *string
	if v := c.IP(); v != "" {
		ip = &v
	}
	var ua *string
	if v := string(c.Request().Header.UserAgent()); v != "" {
		ua = &v
	}
	err := global.CreateSession(c.Context(), db, global.NewSession{
		ID:        sha256Hex(raw),
		UserID:    userID,
		OrgID:     nil,
		Host:      c.Hostname(),
		IP:        ip,
		UserAgent: ua,
		ExpiresAt: time.Now().Add(globalSessionAbsoluteTTL),
	})
	if err != nil {
		return "", err
	}
	c.Cookie(&fiber.Cookie{
		Name:     cookieName(authSessionCookieBase, cookieSecure),
		Value:    raw,
		MaxAge:   int(globalSessionAbsoluteTTL.Seconds()),
		HTTPOnly: true,
		Secure:   cookieSecure,
		SameSite: fiber.CookieSameSiteLaxMode,
		Path:     "/",
	})
	return raw, nil
}

// sanitizeReturnPath enforces task 1.2's open-redirect guard: only a
// same-site relative path is accepted, never an absolute URL or a
// protocol-relative "//host" (which browsers treat as absolute).
func sanitizeReturnPath(p string) string {
	if p == "" || p[0] != '/' || (len(p) > 1 && p[1] == '/') {
		return "/"
	}
	return p
}
