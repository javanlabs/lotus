package authn

import (
	"net/url"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

const (
	handshakeTTL  = 120 * time.Second
	authTicketTTL = 30 * time.Second
)

// Deps are every dependency the auth-host/handoff handlers need. Grouped
// into a struct because internal/web.Deps threads these straight through
// from cmd/lotus, and the list only grows as later Fase 1 tasks add more
// (invitations, tokens, ...). Mailer may be nil or disabled (task 1.7) —
// every caller checks Enabled() before using it, so a nil-vs-disabled
// mailer never needs separate handling.
type Deps struct {
	DB             *sqlx.DB
	Cfg            config.Config
	Renderer       *httpx.Renderer
	OAuthProviders Registry
	CookieSecure   bool
	Mailer         *mailer.Mailer
}

// HandoffStart is step 1 of task 1.2: GET /handoff/start on a tenant host.
// Creates the handshake, sets the verifier cookie, and sends the browser
// to the auth host to actually authenticate.
func HandoffStart(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}

		verifier := randToken(32)
		returnPath := sanitizeReturnPath(c.Query("return"))

		hsID, err := global.CreateHandshake(c.Context(), deps.DB, global.NewHandshake{
			OrgID:        org.ID,
			ReturnPath:   returnPath,
			VerifierHash: sha256Hex(verifier),
			ExpiresAt:    time.Now().Add(handshakeTTL),
		})
		if err != nil {
			return err
		}

		c.Cookie(&fiber.Cookie{
			Name:     cookieName(handshakeCookieBase, deps.CookieSecure),
			Value:    verifier,
			MaxAge:   int(handshakeTTL.Seconds()),
			HTTPOnly: true,
			Secure:   deps.CookieSecure,
			SameSite: fiber.CookieSameSiteLaxMode,
			Path:     "/",
		})

		authURL := deps.Cfg.BaseURL() + "/handoff/authorize?hs=" + hsID.String()
		return c.Redirect(authURL, fiber.StatusFound)
	}
}

// HandoffAuthorize is steps 2-3 of task 1.2: GET /handoff/authorize?hs=
// on the auth host. Runs OAuth if there's no global session yet, then
// checks membership and mints the single-use code.
func HandoffAuthorize(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		hsID, err := uuid.Parse(c.Query("hs"))
		if err != nil {
			return friendlyMessage(c, deps.Renderer, "Link not recognized", "This handoff link is malformed. Please start over.")
		}
		hs, err := global.HandshakeByID(c.Context(), deps.DB, hsID)
		if err != nil {
			return friendlyMessage(c, deps.Renderer, "Link expired", "This link has expired. Please start over.")
		}

		session, ok := currentGlobalSession(c, deps.DB, deps.CookieSecure)
		if !ok {
			return renderLoginPicker(c, deps.Renderer, deps.OAuthProviders, "/handoff/authorize?hs="+hsID.String())
		}

		org, err := global.OrgByID(c.Context(), deps.DB, hs.OrgID)
		if err != nil || org.DeletedAt != nil {
			return friendlyMessage(c, deps.Renderer, "Organization not found", "This organization no longer exists.")
		}

		if _, err := global.ActiveMembership(c.Context(), deps.DB, session.UserID, hs.OrgID); err != nil {
			return friendlyMessage(c, deps.Renderer, "No access",
				"You don't have access to "+org.Name+". Ask an admin to invite you, or sign in with a different account.")
		}

		// TODO(task 4.5): SSO enforcement hook. Once per-org SSO config
		// exists, reject here (instead of minting a code) when
		// org.sso_enforced is true, the membership isn't sso_exempt, and
		// this global session wasn't established via that org's IdP.
		// Enforcing here — at grant time, not at global login — is
		// deliberate: a user in several orgs, only one of which enforces
		// SSO, must still be able to reach the others.

		raw := randToken(32)
		err = global.CreateAuthTicket(c.Context(), deps.DB, global.NewAuthTicket{
			TokenHash:    sha256Hex(raw),
			UserID:       session.UserID,
			OrgID:        hs.OrgID,
			VerifierHash: hs.VerifierHash,
			ReturnPath:   hs.ReturnPath,
			ExpiresAt:    time.Now().Add(authTicketTTL),
		})
		if err != nil {
			return err
		}

		finishURL := deps.Cfg.TenantURL(org.Subdomain) + "/handoff/finish?code=" + raw
		return c.Redirect(finishURL, fiber.StatusFound)
	}
}

// HandoffFinish is step 4 of task 1.2: GET /handoff/finish?code= on the
// tenant host. Atomically consumes the code, checks the verifier cookie
// and the org match, and mints a brand-new tenant session.
func HandoffFinish(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// The code is in this request's own query string — never let it
		// leak onward via a Referer header from whatever this page's
		// response body might load or link to.
		c.Set("Referrer-Policy", "no-referrer")

		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}

		code := c.Query("code")
		verifierCookieName := cookieName(handshakeCookieBase, deps.CookieSecure)
		verifierCookie := c.Cookies(verifierCookieName)
		c.ClearCookie(verifierCookieName)

		if code == "" {
			return friendlyMessage(c, deps.Renderer, "Missing code", "This link is incomplete. Please start over.")
		}

		ticket, err := global.ConsumeAuthTicket(c.Context(), deps.DB, sha256Hex(code))
		if err != nil {
			return friendlyMessage(c, deps.Renderer, "Link expired", "This link has expired or was already used. Please start over.")
		}
		if verifierCookie == "" || sha256Hex(verifierCookie) != ticket.VerifierHash {
			return friendlyMessage(c, deps.Renderer, "Verification failed", "Something didn't match up — please start over.")
		}
		if ticket.OrgID != org.ID {
			return friendlyMessage(c, deps.Renderer, "Wrong organization", "This link isn't valid for this organization.")
		}

		if err := issueTenantSession(c, deps, ticket.UserID, org); err != nil {
			return err
		}
		return c.Redirect(ticket.ReturnPath, fiber.StatusFound)
	}
}

// currentGlobalSession reads the auth-host session cookie and looks up a
// still-live session that is specifically global (OrgID nil) — a tenant
// session cookie of the same name never appears here since each cookie is
// host-only (__Host- prefixed in production) and this handler only ever
// runs on the auth host.
func currentGlobalSession(c *fiber.Ctx, db *sqlx.DB, cookieSecure bool) (*global.Session, bool) {
	raw := c.Cookies(cookieName(authSessionCookieBase, cookieSecure))
	if raw == "" {
		return nil, false
	}
	s, err := global.SessionByID(c.Context(), db, sha256Hex(raw), time.Now().Add(-globalSessionIdleTTL))
	if err != nil || s.OrgID != nil {
		return nil, false
	}
	return s, true
}

func friendlyMessage(c *fiber.Ctx, renderer *httpx.Renderer, heading, body string) error {
	c.Type("html")
	return renderer.Render(c, "pages/message.html", httpx.ViewData{
		Title: heading,
		Data:  struct{ Heading, Body string }{heading, body},
	})
}

func renderLoginPicker(c *fiber.Ctx, renderer *httpx.Renderer, providers Registry, returnPath string) error {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	c.Type("html")
	return renderer.Render(c, "pages/login_picker.html", httpx.ViewData{
		Title: "Log in",
		Data: struct {
			Providers  []string
			ReturnPath string
		}{names, url.QueryEscape(returnPath)},
	})
}

// LoginPage is single mode's entry point into login: the magic-link form,
// any configured OAuth providers, and — under LOTUS_DEV only — a one-click
// list of seeded dummy accounts.
//
// Distinct from renderLoginPicker, which the auth host shows in the middle
// of a handoff. There is no handoff here, and this page has an
// organization in context, which is what magic links need.
func LoginPage(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		names := providerNames(deps.OAuthProviders)

		var devUsers []global.DevUser
		if deps.Cfg.Dev {
			var err error
			if devUsers, err = global.DevUsers(c.Context(), deps.DB, org.ID); err != nil {
				return err
			}
		}

		c.Type("html")
		return deps.Renderer.Render(c, "pages/login.html", httpx.ViewData{
			Title: "Log in",
			Data: struct {
				OrgName    string
				Providers  []string
				ReturnPath string
				DevUsers   []global.DevUser
				Notice     string
			}{org.Name, names, sanitizeReturnPath(c.Query("return")), devUsers, LoginNotice(c.Query("notice"))},
		})
	}
}

// issueTenantSession mints the tenant session cookie for user in org, and
// is the single place that happens. Saas mode reaches it at the end of the
// handoff; single mode has no handoff to reach it from — one origin, so
// login mints the tenant session directly — and both must produce exactly
// the same session shape, or "logged in" would mean two different things
// depending on deployment.
func issueTenantSession(c *fiber.Ctx, deps Deps, userID uuid.UUID, org *global.Org) error {
	raw := randToken(32)
	err := global.CreateSession(c.Context(), deps.DB, global.NewSession{
		ID:        sha256Hex(raw),
		UserID:    userID,
		OrgID:     &org.ID,
		Host:      c.Hostname(),
		PermEpoch: org.PermEpoch,
		ExpiresAt: time.Now().Add(tenantSessionAbsoluteTTL),
	})
	if err != nil {
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
	return nil
}
