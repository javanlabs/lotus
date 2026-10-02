package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

func browserGet(t *testing.T, e qaEnv, raw, path string, header map[string]string) (int, string, string) {
	t.Helper()
	req := reqWithSession(http.MethodGet, e.host, path, raw)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

func TestErrorPages_BrowserGetsAPagePerStatus(t *testing.T) {
	e := newQAEnv(t, "errpages")
	viewer, _ := e.session(t, "viewer@errpages.test", "viewer")
	pid := e.project(t, "Shop")

	cases := []struct {
		name, raw, path string
		status          int
		want            string
	}{
		// Unmatched route: Fiber's own 404, returned as an error.
		{"no route", e.owner, "/no-such-page", 404, "Page not found"},
		// A QA child id that isn't there.
		{"missing case", e.owner, "/projects/" + pid.String() + "/qa/cases/999", 404, "Page not found"},
		// Missing permission: authz.Require's 403.
		{"forbidden", viewer, "/projects/" + pid.String() + "/qa/cases/new", 403, "You don&#39;t have access"},
		{"missing plan", e.owner, "/projects/" + pid.String() + "/qa/plans/" + uuid.NewString(), 404, "Page not found"},
		{"bad plan id", e.owner, "/projects/" + pid.String() + "/qa/plans/x", 404, "Page not found"},
		// A project the organization doesn't have.
		{"unknown project", e.owner, "/projects/" + uuid.NewString() + "/qa", 404, "Page not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, ctype, body := browserGet(t, e, tc.raw, tc.path, nil)
			if status != tc.status {
				t.Fatalf("status = %d, want %d\n%s", status, tc.status, body)
			}
			if !strings.HasPrefix(ctype, "text/html") {
				t.Fatalf("content type = %q, want html", ctype)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("body missing %q:\n%s", tc.want, body)
			}
		})
	}
}

func TestErrorPages_OtherClientsKeepTheirResponse(t *testing.T) {
	e := newQAEnv(t, "errkeep")
	pid := e.project(t, "Shop")
	path := "/projects/" + pid.String() + "/qa/cases/999"

	// htmx: the fragment caller handles the error itself.
	status, ctype, body := browserGet(t, e, e.owner, path, map[string]string{"HX-Request": "true"})
	if status != 404 || strings.HasPrefix(ctype, "text/html") || strings.Contains(body, "Page not found") {
		t.Fatalf("htmx: status=%d type=%q body=%s", status, ctype, body)
	}

	// No Accept: text/html (curl, fetch) keeps the plain response.
	status, body = e.get(t, e.owner, path)
	if status != 404 || strings.Contains(body, "Page not found") {
		t.Fatalf("plain client: status=%d body=%s", status, body)
	}

	// The JSON API never gets HTML, even from a browser.
	status, ctype, _ = browserGet(t, e, "", "/api/qa/projects", nil)
	if status != 401 || strings.HasPrefix(ctype, "text/html") {
		t.Fatalf("api: status=%d type=%q", status, ctype)
	}
}

func TestErrorPages_UnauthorizedOffersSignIn(t *testing.T) {
	d := errorPageFor(401, false)
	if d.LoginHref != "/handoff/start" {
		t.Fatalf("saas login href = %q", d.LoginHref)
	}
	if d := errorPageFor(401, true); d.LoginHref != "/login" {
		t.Fatalf("single login href = %q", d.LoginHref)
	}
	if d := errorPageFor(500, false); d.Heading != "Something went wrong" || d.LoginHref != "" {
		t.Fatalf("500 = %+v", d)
	}
}

// A handler that writes its own error status (writeRepoErr's JSON) instead
// of returning an error goes through the errorPages middleware, not the
// ErrorHandler.
func TestErrorPages_WrittenStatusBecomesAPage(t *testing.T) {
	d := Deps{Renderer: httpx.NewRenderer(os.DirFS("../../web/templates"), true)}
	app := fiber.New()
	app.Use(errorPages(d))
	app.Get("/x", func(c *fiber.Ctx) error { return writeRepoErr(c, repo.ErrNotFound) })
	app.Get("/form", func(c *fiber.Ctx) error {
		c.Status(422).Type("html")
		return c.SendString("<p>name is required</p>")
	})

	get := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if status, body := get("/x"); status != 404 || !strings.Contains(body, "Page not found") {
		t.Fatalf("/x: status=%d body=%s", status, body)
	}
	// An HTML error response is the handler's own message: left alone.
	if status, body := get("/form"); status != 422 || body != "<p>name is required</p>" {
		t.Fatalf("/form: status=%d body=%s", status, body)
	}
}
