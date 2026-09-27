package web

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

var routeParam = regexp.MustCompile(`:[A-Za-z_]+\??|\*`)

// TestGuestShell_CrawlEveryGetRoute visits every registered GET route as a
// guest listed on one project. Only QA pages of that project (and /qa,
// /qa/my, filtered to it) answer 200; a QA page it may not act on is 403;
// everything else is 404, never a page.
func TestGuestShell_CrawlEveryGetRoute(t *testing.T) {
	e := newQAEnv(t, "acme-guest-crawl")
	_, adminID := e.session(t, "admin@acme.example", "admin")
	listed, other := e.project(t, "Portal"), e.project(t, "Mobile")
	guest, _, _ := inviteAndAccept(t, e, e.relogin(t, adminID), "client@bank.example", listed)

	var seen []string
	for _, route := range e.app.GetRoutes(true) {
		if route.Method != fiber.MethodGet || guestCrawlPublic(route.Handlers) {
			continue
		}
		for _, project := range []uuid.UUID{listed, other} {
			path := strings.Replace(route.Path, ":id", project.String(), 1)
			path = routeParam.ReplaceAllString(path, uuid.NewString())
			status, _ := e.get(t, guest, path)
			qa := strings.HasPrefix(route.Path, "/projects/:id/qa") || route.Path == "/qa" || strings.HasPrefix(route.Path, "/qa/")
			switch {
			case qa && project == listed:
				// Its own QA: pages answer 200, unknown ids 404, write
				// pages it lacks the permission for 403.
				if status != http.StatusOK && status != http.StatusNotFound && status != http.StatusForbidden {
					t.Errorf("guest GET %s = %d, want 200, 403 or 404", route.Path, status)
				}
			case route.Path == "/projects":
				// The landing page sends a guest home (TestGuestShell_HomeIsMyWork).
			case status != http.StatusNotFound:
				t.Errorf("guest GET %s (project listed=%v) = %d, want 404", route.Path, project == listed, status)
			}
			seen = append(seen, route.Path)
			if !strings.Contains(route.Path, ":id") {
				break // no project in the path: one visit is enough
			}
		}
	}
	sort.Strings(seen)
	if len(seen) < 20 {
		t.Fatalf("crawled only %d routes: %v", len(seen), seen)
	}
}

// TestGuestShell_HomeIsMyWork: a guest's home is /qa/my, the sidebar only
// offers QA, and a guest missing a QA permission still gets 403 (the page
// exists for it; the action does not).
func TestGuestShell_HomeIsMyWork(t *testing.T) {
	e := newQAEnv(t, "acme-guest-home")
	_, adminID := e.session(t, "admin@acme.example", "admin")
	listed := e.project(t, "Portal")
	guest, _, _ := inviteAndAccept(t, e, e.relogin(t, adminID), "client@bank.example", listed)

	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, "/projects", guest))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/qa/my" {
		t.Fatalf("guest /projects = %d → %q, want 302 → /qa/my", resp.StatusCode, resp.Header.Get("Location"))
	}
	status, body := e.get(t, guest, "/qa/my")
	if status != http.StatusOK {
		t.Fatalf("guest /qa/my = %d", status)
	}
	for _, hidden := range []string{`href="/settings`, `href="/projects"`, `href="/metrics`} {
		if strings.Contains(body, hidden) {
			t.Errorf("guest sidebar links %s", hidden)
		}
	}
	if !strings.Contains(body, `href="/projects/`+listed.String()+`/qa"`) {
		t.Errorf("guest sidebar has no link to its project's QA")
	}
	if status, _ := e.get(t, guest, "/projects/"+listed.String()+"/qa/cases/new"); status != http.StatusForbidden {
		t.Errorf("guest new case page = %d, want 403", status)
	}
}

// guestCrawlPublic: public routes (login, invitations, static, health)
// decide nothing about a principal, so the crawl leaves them out.
func guestCrawlPublic(handlers []fiber.Handler) bool {
	kind, _ := decisionFor(handlers)
	return kind == "public"
}
