package web

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
)

// Flash messages are the app's one-shot "that worked" notices: a mutation
// handler names a message code, redirects, and the very next page render
// picks the code up, resolves it to copy, and hands it to the shell as a
// toast.
//
// The cookie carries a CODE, never the message text. There is no signing
// key in this app (sessions are random tokens hashed into the database),
// so a text-carrying cookie would let anyone put arbitrary words in their
// own toast. Codes bound that to the set of messages Lotus already ships,
// and keep the copy in Go where the design system's voice rules apply —
// flat, factual, sentence case, "·" between peers.
//
// This deliberately does NOT replace settings_tokens.go's tokenFlashes.
// That one carries a plaintext secret and must stay server-side and
// single-read; this is for messages that are safe in a cookie.
const flashCookie = "lotus_flash"

// flashMessages maps a code to its tone and copy. Format verbs are filled
// from the single optional argument setFlash takes; the value is escaped
// by html/template at render, so a tampered argument can only change a
// word in a message the reader is showing themselves.
var flashMessages = map[string]struct {
	Tone   string
	Format string
}{
	"connection.created":    {"pass", "Connection created"},
	"connection.deleted":    {"pass", "Connection deleted"},
	"connection.unbound":    {"pass", "Connection deleted · %s bindings removed"},
	"connection.rotated":    {"pass", "Secret rotated"},
	"member.invited":        {"pass", "Invitation sent to %s"},
	"member.role":           {"pass", "Role updated"},
	"member.removed":        {"pass", "Member removed"},
	"invitation.resent":     {"pass", "Invitation resent"},
	"invitation.revoked":    {"pass", "Invitation revoked"},
	"folder.created":        {"pass", "Folder %s created"},
	"folder.renamed":        {"pass", "Folder renamed to %s"},
	"folder.moved":          {"pass", "Folder %s moved"},
	"folder.deleted":        {"pass", "Folder %s deleted"},
	"project.moved":         {"pass", "Project moved"},
	"projects.moved":        {"pass", "%s projects moved"},
	"qa.section.created":    {"pass", "Section %s created"},
	"qa.section.renamed":    {"pass", "Section renamed to %s"},
	"qa.section.moved":      {"pass", "Section %s moved"},
	"qa.section.archived":   {"pass", "Section %s archived"},
	"qa.cases.moved":        {"pass", "%s cases moved"},
	"qa.case.created":       {"pass", "%s created"},
	"qa.case.saved":         {"pass", "%s saved"},
	"qa.case.archived":      {"pass", "%s archived"},
	"qa.case.restored":      {"pass", "%s restored"},
	"qa.cases.imported":     {"pass", "%s cases imported"},
	"qa.scenario.created":   {"pass", "Scenario %s created"},
	"qa.scenario.saved":     {"pass", "Scenario %s saved"},
	"qa.scenario.archived":  {"pass", "Scenario %s archived"},
	"qa.plan.created":       {"pass", "Plan %s created"},
	"qa.plan.saved":         {"pass", "Plan %s saved"},
	"qa.plan.closed":        {"pass", "Plan %s closed"},
	"qa.run.created":        {"pass", "Run %s created"},
	"qa.run.closed":         {"pass", "Run %s closed"},
	"qa.run.saved":          {"pass", "Run %s saved"},
	"qa.run.deleted":        {"pass", "Run %s deleted"},
	"qa.items.assigned":     {"pass", "%s items assigned"},
	"qa.items.removed":      {"pass", "%s items removed"},
	"qa.issue.reported":     {"pass", "%s reported"},
	"qa.issue.saved":        {"pass", "%s saved"},
	"project.guest.added":   {"pass", "Guest added"},
	"project.guest.removed": {"pass", "Guest removed"},
	"qa.signoff.recorded":   {"pass", "Signed off: %s"},
	"qa.share.revoked":      {"pass", "Link revoked"},
}

// setFlash records a message for the next render. Call it immediately
// before the redirect that follows a successful mutation. At most one
// argument, which fills the code's format verb.
func setFlash(c *fiber.Ctx, code string, arg ...string) {
	v := code
	if len(arg) > 0 {
		v += "|" + url.QueryEscape(arg[0])
	}
	c.Cookie(&fiber.Cookie{
		Name:     flashCookie,
		Value:    v,
		Path:     "/",
		HTTPOnly: true,
		SameSite: "Lax",
		// Short: it exists only to survive one redirect. A stale flash
		// showing up on a page the user navigated to minutes later would
		// be a bug, not a feature.
		Expires: time.Now().Add(30 * time.Second),
	})
}

// takeFlash reads and clears the flash. Unknown codes resolve to nil
// rather than rendering anything — a cookie naming a message this build
// does not ship is simply dropped.
func takeFlash(c *fiber.Ctx) *httpx.Flash {
	raw := c.Cookies(flashCookie)
	if raw == "" {
		return nil
	}
	// Clear it whatever happens next, so a malformed value cannot wedge
	// itself on every subsequent page.
	c.Cookie(&fiber.Cookie{
		Name: flashCookie, Value: "", Path: "/",
		HTTPOnly: true, SameSite: "Lax",
		Expires: time.Now().Add(-time.Hour), MaxAge: -1,
	})
	// Expiring the response cookie only stops the NEXT request carrying
	// it; the value is still sitting on this one. Drop it here too, so a
	// second reader in the same request gets nothing rather than a
	// duplicate toast. renderPage is the only caller today — this makes
	// "single-use" a property of the function instead of a convention its
	// callers have to keep.
	c.Request().Header.DelCookie(flashCookie)

	code, arg, _ := strings.Cut(raw, "|")
	msg, ok := flashMessages[code]
	if !ok {
		return nil
	}
	text := msg.Format
	if strings.Contains(text, "%s") {
		decoded, err := url.QueryUnescape(arg)
		if err != nil {
			return nil
		}
		text = fmt.Sprintf(text, decoded)
	}
	return &httpx.Flash{Tone: msg.Tone, Text: text}
}

// redirectAfterMutation ends a successful mutation.
//
// A plain form post gets the usual POST-redirect-GET. An htmx post gets
// HX-Refresh instead, which makes the browser reload the page it is on —
// that refreshes whatever list the modal just changed AND lets the flash
// cookie surface as a toast on the way through, so the success path has
// one implementation rather than a redirect version and a swap version
// that drift apart. The cookie rides the 204 fine; Set-Cookie is a header
// and does not need a body.
func redirectAfterMutation(c *fiber.Ctx, to string) error {
	if c.Get("HX-Request") != "" {
		c.Set("HX-Refresh", "true")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect(to, fiber.StatusFound)
}

// renderPage is the single choke point for full-page renders. Every page
// goes through it so the flash is picked up in one place rather than in
// nineteen handlers — and so partial/fragment renders, which must not
// consume a flash meant for the page that follows them, keep using
// Renderer.RenderPartial directly.
func renderPage(c *fiber.Ctx, d Deps, page string, vd httpx.ViewData) error {
	vd.Flash = takeFlash(c)
	if vd.Nav != nil {
		if d.Cfg.Features.QA {
			guestNav(c, vd.Nav)
			qaNavCount(c, vd.Nav)
		} else {
			withoutQANav(vd.Nav)
		}
	}
	return d.Renderer.Render(c, page, vd)
}

// withoutQANav drops the QA Tools entry when LOTUS_FEATURE_QA is off.
// Here rather than in buildNav because every page builds its own nav and
// none of them has the config to hand; renderPage is the one place every
// full page passes through, the same reason guestNav lives here.
func withoutQANav(nav *httpx.Nav) {
	items := nav.Items[:0]
	for _, item := range nav.Items {
		if item.Key != "qa_tools" {
			items = append(items, item)
		}
	}
	nav.Items = items
}
