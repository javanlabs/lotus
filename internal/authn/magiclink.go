package authn

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

const (
	// loginTokenTTL is short because the link is a bearer credential: it
	// sits in an inbox, a shell history, or a browser history until it
	// expires. Long enough to walk to your mail client, not long enough to
	// still work tomorrow.
	loginTokenTTL = 15 * time.Minute

	loginRateWindow = 10 * time.Minute
	loginRateMax    = 5
)

// RequestMagicLink issues a single-use login link for an address that
// already holds a membership.
//
// It answers identically whether or not the address belongs to a member.
// That is the point: a differing response turns this endpoint into an
// oracle for "does this person have an account here", which is exactly the
// enumeration a login form must not offer. The link is only ever created
// for a real member.
func RequestMagicLink(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		email := strings.ToLower(strings.TrimSpace(c.FormValue("email")))
		returnPath := sanitizeReturnPath(c.FormValue("return"))

		link, err := mintMagicLink(c, deps, org, email, returnPath)
		if err != nil {
			return err
		}
		return renderMagicLinkSent(c, deps, email, link)
	}
}

// mintMagicLink returns the absolute link, or "" when no link should
// exist — no such verified user, no active membership, or the rate limit
// is hit. A "" is never an error the caller surfaces: see RequestMagicLink.
func mintMagicLink(c *fiber.Ctx, deps Deps, org *global.Org, email, returnPath string) (string, error) {
	if email == "" {
		return "", nil
	}
	user, err := global.UserByVerifiedEmail(c.Context(), deps.DB, email)
	if err != nil {
		return "", nil // unknown or unverified address — silently no link
	}
	if _, err := global.ActiveMembership(c.Context(), deps.DB, user.ID, org.ID); err != nil {
		return "", nil // not a member of this organization
	}

	n, err := global.CountRecentLoginTokens(c.Context(), deps.DB, user.ID, time.Now().Add(-loginRateWindow))
	if err != nil {
		return "", err
	}
	if n >= loginRateMax {
		return "", nil
	}
	return issueMagicLink(c, deps, user.ID, org, returnPath)
}

// issueMagicLink creates the token row and returns the absolute link. It
// performs no eligibility checks of its own — every caller must have
// established that this user may log in.
func issueMagicLink(c *fiber.Ctx, deps Deps, userID uuid.UUID, org *global.Org, returnPath string) (string, error) {
	raw := randToken(32)
	err := global.CreateLoginToken(c.Context(), deps.DB, global.NewLoginToken{
		ID:         sha256Hex(raw),
		UserID:     userID,
		OrgID:      org.ID,
		ReturnPath: returnPath,
		ExpiresAt:  time.Now().Add(loginTokenTTL),
	})
	if err != nil {
		return "", err
	}
	return deps.Cfg.BaseURL() + "/login/email/verify?token=" + raw, nil
}

// VerifyMagicLink redeems a link and starts the session.
func VerifyMagicLink(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// The token is in this request's own query string — never let it
		// leak onward through a Referer header.
		c.Set("Referrer-Policy", "no-referrer")

		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		raw := c.Query("token")
		if raw == "" {
			return friendlyMessage(c, deps.Renderer, "Link incomplete", "This login link is missing its token. Please request a new one.")
		}

		tok, err := global.ConsumeLoginToken(c.Context(), deps.DB, sha256Hex(raw))
		if err != nil {
			if errors.Is(err, global.ErrLoginTokenInvalid) {
				return friendlyMessage(c, deps.Renderer, "Link expired",
					"This login link has expired or was already used. Please request a new one.")
			}
			return err
		}
		if tok.OrgID != org.ID {
			return friendlyMessage(c, deps.Renderer, "Wrong organization", "This link isn't valid here.")
		}

		// Re-check membership at redemption: the token was minted for a
		// member, but that was up to 15 minutes ago and access can be
		// revoked in between.
		if _, err := global.ActiveMembership(c.Context(), deps.DB, tok.UserID, org.ID); err != nil {
			return friendlyMessage(c, deps.Renderer, "No access", "Your access to this organization has been removed.")
		}

		if err := issueTenantSession(c, deps, tok.UserID, org); err != nil {
			return err
		}
		if err := global.TouchLastLogin(c.Context(), deps.DB, tok.UserID); err != nil {
			return err
		}
		return c.Redirect(tok.ReturnPath, fiber.StatusFound)
	}
}

// DevLoginAs is the one-click "log in as <dummy user>" shortcut, and is
// gated three independent ways:
//
//  1. internal/web does not register the route at all unless cfg.Dev;
//  2. this handler re-checks cfg.Dev itself, so a mis-wired route table
//     still cannot reach it;
//  3. it will only impersonate an account whose address ends in
//     global.DevUserSuffix (@lotus.test — reserved by RFC 2606, so it can
//     never belong to a real person).
//
// It deliberately goes through the same token machinery as a real magic
// link rather than minting a session directly: one redemption path means
// one place where session-issuing rules live.
func DevLoginAs(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !deps.Cfg.Dev {
			return fiber.ErrNotFound
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		userID, err := uuid.Parse(c.Params("user"))
		if err != nil {
			return fiber.ErrNotFound
		}

		user, err := global.UserByID(c.Context(), deps.DB, userID)
		if err != nil || user.Email == nil || !strings.HasSuffix(*user.Email, global.DevUserSuffix) {
			return fiber.ErrNotFound
		}
		if _, err := global.ActiveMembership(c.Context(), deps.DB, user.ID, org.ID); err != nil {
			return fiber.ErrNotFound
		}

		link, err := issueMagicLink(c, deps, user.ID, org, sanitizeReturnPath(c.FormValue("return")))
		if err != nil {
			return err
		}
		return c.Redirect(link, fiber.StatusFound)
	}
}

// renderMagicLinkSent always says the same thing, whether or not a link was
// actually created — see RequestMagicLink on why.
//
// The link is echoed into the page only under LOTUS_DEV: locally there is
// usually no SMTP to receive it, and the alternative is a login flow that
// cannot be completed at all. Outside dev the link exists solely in the
// email, even when SMTP is unconfigured and it therefore reaches nobody —
// a login you cannot finish is a far smaller problem than one anybody can.
func renderMagicLinkSent(c *fiber.Ctx, deps Deps, email, link string) error {
	if link != "" && deps.Mailer.Enabled() {
		_ = deps.Mailer.Send(mailer.Message{
			To:      mailer.Address{Email: email},
			Subject: "Your Lotus login link",
			HTML: "<p>Here is your login link. It expires in 15 minutes and works once.</p>" +
				"<p><a href=\"" + link + "\">Log in to Lotus</a></p>",
		})
	}
	devLink := ""
	if deps.Cfg.Dev {
		devLink = link
	}
	c.Type("html")
	return deps.Renderer.Render(c, "pages/login_sent.html", httpx.ViewData{
		Title: "Check your email",
		Data: struct {
			Email   string
			DevLink string
		}{email, devLink},
	})
}
