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
// guest listed on one project, naming each project by slug and by id. Only
// QA pages of that project (and /qa, /qa/my, filtered to it) answer 200 —
// or, by id, 301 to the slug URL; a QA page it may not act on is 403;
// everything else is 404, never a page — nor a redirect that would show
// another project's slug.
func TestGuestShell_CrawlEveryGetRoute(t *testing.T) {
	e := newQAEnv(t, "acme-guest-crawl")
	_, adminID := e.session(t, "admin@acme.example", "admin")
	listed, other := e.project(t, "Portal"), e.project(t, "Mobile")
	guest, _, _ := inviteAndAccept(t, e, e.relogin(t, adminID), "client@bank.example", listed)
	refs := map[uuid.UUID][]string{
		listed: {"Portal", listed.String()},
		other:  {"Mobile", other.String()},
	}

	var seen []string
	for _, route := range e.app.GetRoutes(true) {
		if route.Method != fiber.MethodGet || guestCrawlPublic(route.Handlers) {
			continue
		}
	projects:
		for _, project := range []uuid.UUID{listed, other} {
			for _, ref := range refs[project] {
				path := strings.Replace(route.Path, ":id", ref, 1)
				path = routeParam.ReplaceAllString(path, uuid.NewString())
				status, _ := e.get(t, guest, path)
				guestCrawlCheck(t, route.Path, project == listed, ref == project.String(), status)
				if !strings.Contains(route.Path, ":id") {
					break projects // no project in the path: one visit is enough
				}
			}
		}
		seen = append(seen, route.Path)
	}
	sort.Strings(seen)
	if len(seen) < 20 {
		t.Fatalf("crawled only %d routes: %v", len(seen), seen)
	}
}

// guestCrawlCheck is TestGuestShell_CrawlEveryGetRoute's verdict on one
// visit: route, whether its project is the guest's, whether the project
// was named by id, and the status the guest got.
func guestCrawlCheck(t *testing.T, route string, listed, byID bool, status int) {
	t.Helper()
	qa := strings.HasPrefix(route, "/projects/:id/qa") || route == "/qa" || strings.HasPrefix(route, "/qa/")
	switch {
	case qa && listed && byID && strings.HasPrefix(route, "/projects/"):
		// Its own QA by id: the page moves to its slug URL, unless the
		// guest may not open it at all.
		if status != http.StatusMovedPermanently && status != http.StatusForbidden {
			t.Errorf("guest GET %s by id = %d, want 301 or 403", route, status)
		}
	case qa && listed:
		// Its own QA: pages answer 200, unknown ids 404, write
		// pages it lacks the permission for 403.
		if status != http.StatusOK && status != http.StatusNotFound && status != http.StatusForbidden {
			t.Errorf("guest GET %s = %d, want 200, 403 or 404", route, status)
		}
	case route == "/projects" || route == "/dashboard":
		// The landing pages send a guest home (TestGuestShell_HomeIsMyWork,
		// TestHomeDashboard_LandingAndGuests).
	case strings.HasPrefix(route, "/api/qa/"):
		// The QA API takes API tokens only; a browser session is
		// no credential there (TestQAAPI_TokensOnlyAndJSONErrors).
		if status != http.StatusUnauthorized {
			t.Errorf("guest session GET %s = %d, want 401", route, status)
		}
	case status != http.StatusNotFound:
		t.Errorf("guest GET %s (project listed=%v, by id=%v) = %d, want 404", route, listed, byID, status)
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
	if !strings.Contains(body, `href="/projects/Portal/qa"`) {
		t.Errorf("guest sidebar has no link to its project's QA")
	}
	if !strings.Contains(body, `data-testid="guest-notice"`) {
		t.Errorf("guest page has no access notice")
	}
	if _, body := e.get(t, e.owner, "/qa/my"); strings.Contains(body, `data-testid="guest-notice"`) {
		t.Errorf("owner page shows the guest access notice")
	}
	if status, _ := e.get(t, guest, e.projectPath(t, listed)+"/qa/cases/new"); status != http.StatusForbidden {
		t.Errorf("guest new case page = %d, want 403", status)
	}
}

// guestCrawlPublic: public routes (login, invitations, static, health)
// decide nothing about a principal, so the crawl leaves them out.
func guestCrawlPublic(handlers []fiber.Handler) bool {
	kind, _ := decisionFor(handlers)
	return kind == "public"
}
