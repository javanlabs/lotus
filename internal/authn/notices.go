package authn

import (
	"net/url"

	"github.com/gofiber/fiber/v2"
)

// Login notices are the reasons a sign-in attempt can end back on the login
// page. They travel as a short code in the query string and are turned
// into text here, never taken from the URL — so a link carrying
// ?notice=<anything> cannot put words of an attacker's choosing on a
// Lotus-branded login page.
const (
	NoticeLoginExpired   = "login_expired"
	NoticeLoginCancelled = "login_cancelled"
	NoticeLoginFailed    = "login_failed"
	NoticeOwnerPending   = "owner_pending"
)

var loginNotices = map[string]string{
	NoticeLoginExpired: "That sign-in link expired or was already used. " +
		"This happens when the sign-in page stays open for more than a few minutes. Please sign in again.",
	NoticeLoginCancelled: "Sign-in was cancelled at your identity provider. You can try again whenever you are ready.",
	NoticeLoginFailed:    "We couldn't finish signing you in with your identity provider. Please try again.",
	NoticeOwnerPending: "This Lotus instance is waiting for its first Owner. " +
		"Sign in with the email address that was nominated when it was set up.",
}

// LoginNotice returns the message for a notice code, or "" for a code it
// does not know — which is how an unrecognised or tampered value is
// silently ignored rather than echoed.
func LoginNotice(code string) string {
	return loginNotices[code]
}

// loginLanding is where a person lands to start signing in again. Single
// mode has a real login page. Saas mode has no login page on the auth
// host — sign-in starts from a tenant — so the apex is the nearest place
// that can explain what happened.
func loginLanding(deps Deps) string {
	if deps.Cfg.IsSingle() {
		return "/login"
	}
	return "/"
}

// redirectToLogin ends a failed sign-in back on the landing page with a
// notice, instead of a bare error string the browser renders as the whole
// page. returnPath is carried through so that trying again still lands
// where the person was originally going.
func redirectToLogin(c *fiber.Ctx, deps Deps, notice, returnPath string) error {
	q := url.Values{"notice": {notice}}
	if rp := sanitizeReturnPath(returnPath); rp != "/" {
		q.Set("return", rp)
	}
	return c.Redirect(loginLanding(deps)+"?"+q.Encode(), fiber.StatusFound)
}
