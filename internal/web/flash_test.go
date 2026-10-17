package web

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
)

// flashRoundTrip drives setFlash on one request and takeFlash on the next,
// carrying the cookie across the way a browser would.
func flashRoundTrip(t *testing.T, set func(c *fiber.Ctx)) *httpx.Flash {
	t.Helper()
	app := fiber.New()
	app.Get("/set", func(c *fiber.Ctx) error { set(c); return c.SendStatus(fiber.StatusNoContent) })

	var got *httpx.Flash
	var cleared bool
	app.Get("/read", func(c *fiber.Ctx) error {
		got = takeFlash(c)
		cleared = takeFlash(c) == nil
		return c.SendStatus(fiber.StatusNoContent)
	})

	setResp, err := app.Test(httptest.NewRequest("GET", "/set", nil), -1)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	cookie := setResp.Header.Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("no flash cookie was set")
	}
	req := httptest.NewRequest("GET", "/read", nil)
	req.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
	readResp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !cleared {
		t.Error("a second read in the same request saw the flash again; it would toast twice")
	}
	// And the browser must be told to drop it, or the next page repeats it.
	if got := readResp.Header.Get("Set-Cookie"); !strings.Contains(got, flashCookie+"=;") &&
		!strings.Contains(got, "Max-Age=0") && !strings.Contains(got, "expires=") {
		t.Errorf("read did not clear the cookie for the next request: %q", got)
	}
	return got
}

func TestFlash_ResolvesCodeToCopy(t *testing.T) {
	t.Parallel()
	got := flashRoundTrip(t, func(c *fiber.Ctx) { setFlash(c, "connection.created") })
	if got == nil {
		t.Fatal("no flash came back")
	}
	if got.Text != "Connection created" || got.Tone != "pass" {
		t.Errorf("got %+v, want {pass Connection created}", got)
	}
}

func TestFlash_FillsTheFormatArgument(t *testing.T) {
	t.Parallel()
	got := flashRoundTrip(t, func(c *fiber.Ctx) { setFlash(c, "member.invited", "rizky@alurkerja.id") })
	if got == nil {
		t.Fatal("no flash came back")
	}
	if got.Text != "Invitation sent to rizky@alurkerja.id" {
		t.Errorf("Text = %q", got.Text)
	}
}

// An argument with a separator or a percent sign must not corrupt the
// message: the value is escaped into the cookie, and the format string
// comes from our own table rather than from the cookie.
func TestFlash_ArgumentCannotBreakTheEncoding(t *testing.T) {
	t.Parallel()
	got := flashRoundTrip(t, func(c *fiber.Ctx) { setFlash(c, "member.invited", "a|b%sc@d.io") })
	if got == nil {
		t.Fatal("no flash came back")
	}
	if got.Text != "Invitation sent to a|b%sc@d.io" {
		t.Errorf("Text = %q", got.Text)
	}
}

// The cookie is not signed, so it must be inert when tampered with: a code
// this build does not ship resolves to nothing rather than rendering
// whatever the cookie says.
func TestFlash_UnknownCodeIsDropped(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	var got *httpx.Flash
	app.Get("/read", func(c *fiber.Ctx) error { got = takeFlash(c); return c.SendStatus(204) })
	req := httptest.NewRequest("GET", "/read", nil)
	req.Header.Set("Cookie", flashCookie+"=totally.made.up|<script>")
	if _, err := app.Test(req, -1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != nil {
		t.Errorf("a tampered cookie produced a flash: %+v", got)
	}
}

// Every code in the table must be reachable: a format verb with no
// argument (or an argument with no verb) is a copy bug that would only
// show up as "%!s(MISSING)" in front of a user.
func TestFlash_EveryCodeRendersWithItsOwnArity(t *testing.T) {
	t.Parallel()
	for code, msg := range flashMessages {
		var got *httpx.Flash
		if strings.Contains(msg.Format, "%s") {
			got = flashRoundTrip(t, func(c *fiber.Ctx) { setFlash(c, code, "sample") })
		} else {
			got = flashRoundTrip(t, func(c *fiber.Ctx) { setFlash(c, code) })
		}
		if got == nil {
			t.Errorf("%s: no flash", code)
			continue
		}
		if strings.Contains(got.Text, "%!") || strings.Contains(got.Text, "MISSING") {
			t.Errorf("%s rendered %q", code, got.Text)
		}
		if got.Tone == "" {
			t.Errorf("%s has no tone", code)
		}
	}
}

// redirectAfterMutation has to answer htmx differently from a plain form
// post: a redirect inside an htmx swap would be followed by htmx and
// swapped into the modal, not navigated to.
func TestRedirectAfterMutation_HtmxGetsRefreshNotRedirect(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	app.Post("/go", func(c *fiber.Ctx) error { return redirectAfterMutation(c, "/settings/members") })

	plain := httptest.NewRequest("POST", "/go", nil)
	resp, err := app.Test(plain, -1)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if resp.StatusCode != fiber.StatusFound || resp.Header.Get("Location") != "/settings/members" {
		t.Errorf("plain post: status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	hx := httptest.NewRequest("POST", "/go", nil)
	hx.Header.Set("HX-Request", "true")
	resp, err = app.Test(hx, -1)
	if err != nil {
		t.Fatalf("htmx: %v", err)
	}
	if resp.StatusCode != fiber.StatusNoContent {
		t.Errorf("htmx post: status %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Error("htmx post did not ask the page to refresh, so the flash would never be shown")
	}
	if resp.Header.Get("Location") != "" {
		t.Error("htmx post sent a Location; htmx would follow it into the modal")
	}
}

// The modal's two bodies are rendered both inline by their page and
// standalone by an htmx response, so RenderNamed must be able to execute
// them on their own — a missing component definition or a renamed field
// only shows up here.
func TestModalForms_RenderStandaloneWithTheirViews(t *testing.T) {
	t.Parallel()
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)

	t.Run("connection_form preserves input and shows the error", func(t *testing.T) {
		var sb strings.Builder
		view := buildConnectionForm("sonarqube", "billing gate",
			map[string]string{"base_url": "https://sonar.example"}, "Every connection field is required.")
		if err := r.RenderNamed(&sb, "connection_form", view); err != nil {
			t.Fatalf("render: %v", err)
		}
		out := sb.String()
		for _, want := range []string{
			`id="connection-form"`,
			`hx-post="/settings/connections"`,
			`hx-target="#connection-form"`,
			`Every connection field is required.`,
			`value="billing gate"`,
			`https://sonar.example`,
			`/settings/connections/form?kind=`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q", want)
			}
		}
	})

	t.Run("invite_form preserves the address", func(t *testing.T) {
		var sb strings.Builder
		view := buildInviteForm("rizky@alurkerja.id", "admin", "An invitation to this email is already pending.")
		if err := r.RenderNamed(&sb, "invite_form", view); err != nil {
			t.Fatalf("render: %v", err)
		}
		out := sb.String()
		for _, want := range []string{
			`id="invite-form"`,
			`hx-post="/settings/members/invite"`,
			`value="rizky@alurkerja.id"`,
			`An invitation to this email is already pending.`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q", want)
			}
		}
	})
}

// buildInviteForm defaults an unknown role rather than emitting a select
// with nothing chosen, which is what a native select would have done.
func TestBuildInviteForm_DefaultsUnknownRole(t *testing.T) {
	t.Parallel()
	if got := buildInviteForm("a@b.io", "wizard", "").Role; got != "member" {
		t.Errorf("Role = %q, want member", got)
	}
	if got := buildInviteForm("a@b.io", "admin", "").Role; got != "admin" {
		t.Errorf("Role = %q, want admin", got)
	}
}
