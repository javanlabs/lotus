package authn

import (
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
		return c.Redirect(p.AuthCodeURL(state, challenge), fiber.StatusFound)
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

		if stateParam == "" || cookieState == "" || stateParam != cookieState {
			return fiber.NewError(fiber.StatusBadRequest, "invalid or missing oauth state")
		}

		consumed, err := global.ConsumeOAuthState(c.Context(), db, sha256Hex(stateParam))
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "expired or already-used oauth state")
		}
		if consumed.Provider != p.Name() {
			return fiber.NewError(fiber.StatusBadRequest, "provider mismatch")
		}

		code := c.Query("code")
		if code == "" {
			return fiber.NewError(fiber.StatusBadRequest, "missing code")
		}
		accessToken, err := p.Exchange(c.Context(), code, consumed.CodeVerifier)
		if err != nil {
			return err
		}
		identity, err := p.FetchIdentity(c.Context(), accessToken)
		if err != nil {
			return err
		}

		user, _, err := ResolveUser(c.Context(), db, p.Name(), identity)
		if err != nil {
			return err
		}
		if err := global.TouchLastLogin(c.Context(), db, user.ID); err != nil {
			return err
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
