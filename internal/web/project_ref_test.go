package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

// get3 fetches path and returns status, Location and body — without
// following a redirect, which is what these tests look at.
func (e qaEnv) get3(t *testing.T, raw, path string) (int, string, string) {
	t.Helper()
	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, path, raw))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(body)
}

func (e qaEnv) component(t *testing.T, projectID uuid.UUID, slug string) *repo.Component {
	t.Helper()
	comp, err := repo.CreateComponent(context.Background(), e.s, repo.NewComponent{ProjectID: projectID, Slug: slug, Name: slug, Kind: "backend"})
	if err != nil {
		t.Fatalf("create component %q: %v", slug, err)
	}
	return comp
}

func TestProjectRef_SlugOpensThePageAndAnIDMovesThere(t *testing.T) {
	e := newQAEnv(t, "acme-ref")
	p := e.project(t, "portal")

	if status, _, body := e.get3(t, e.owner, "/projects/portal"); status != http.StatusOK || !strings.Contains(body, `href="/projects/portal/qa"`) {
		t.Fatalf("by slug = %d; want 200 with slug links", status)
	}
	// An id moves to the slug URL, keeping the rest of the path and the
	// query.
	cases := map[string]string{
		"/projects/" + p.String():                                    "/projects/portal",
		"/projects/" + p.String() + "/qa":                            "/projects/portal/qa",
		"/projects/" + p.String() + "/qa/cases?section=x&q=a%20b":    "/projects/portal/qa/cases?section=x&q=a%20b",
		"/projects/" + p.String() + "/qa/plans/" + uuid.Nil.String(): "/projects/portal/qa/plans/" + uuid.Nil.String(),
		"/projects/" + p.String() + "/components/new":                "/projects/portal/components/new",
	}
	for from, to := range cases {
		status, loc, _ := e.get3(t, e.owner, from)
		if status != http.StatusMovedPermanently || loc != to {
			t.Errorf("GET %s = %d → %q; want 301 → %q", from, status, loc, to)
		}
	}
	for _, path := range []string{"/projects/nope", "/projects/nope/qa", "/projects/" + uuid.NewString(), "/projects/" + uuid.NewString() + "/qa"} {
		if status, _, _ := e.get3(t, e.owner, path); status != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", path, status)
		}
	}
}

func TestProjectRef_GuestCannotTellAnUnlistedProjectFromAMissingOne(t *testing.T) {
	e := newQAEnv(t, "acme-ref-guest")
	mine, other := e.project(t, "mine"), e.project(t, "theirs")
	guest, guestID := e.session(t, "client@bank.example", "guest")
	if err := repo.AddProjectMember(context.Background(), e.s, mine, guestID); err != nil {
		t.Fatal(err)
	}

	if status, _, _ := e.get3(t, guest, "/projects/mine/qa"); status != http.StatusOK {
		t.Fatalf("guest on its project by slug = %d", status)
	}
	if status, loc, _ := e.get3(t, guest, "/projects/"+mine.String()+"/qa"); status != http.StatusMovedPermanently || loc != "/projects/mine/qa" {
		t.Fatalf("guest on its project by id = %d → %q", status, loc)
	}
	// The unlisted project by slug and by id — the id never redirects,
	// which would show its slug — answers exactly as a missing one.
	_, _, missing := e.get3(t, guest, "/projects/missing/qa")
	for _, path := range []string{"/projects/theirs/qa", "/projects/" + other.String() + "/qa", "/projects/" + uuid.NewString() + "/qa"} {
		status, loc, body := e.get3(t, guest, path)
		if status != http.StatusNotFound || loc != "" || body != missing {
			t.Errorf("guest GET %s = %d → %q; want the same 404 as a missing project", path, status, loc)
		}
	}
}

func TestProjectRef_ComponentAndMetricURLs(t *testing.T) {
	e := newQAEnv(t, "acme-ref-comp")
	projectID, componentID, _, bindingID := seedMetricChromeFixture(t, e.db, e.orgID, "gitlab", "grp/api") // project p, component c
	otherProject := e.project(t, "other")
	stranger := e.component(t, otherProject, "stranger")

	// A component opens on its first bound kind — a redirect, not a move:
	// which kind that is changes as bindings do.
	if status, loc, _ := e.get3(t, e.owner, "/projects/p/components/c"); status != http.StatusFound || loc != "/projects/p/components/c/metrics/gitlab" {
		t.Fatalf("component by slugs = %d → %q; want 302 → its gitlab tab", status, loc)
	}
	// A metric page is named by its kind: a component has one live binding
	// of each (migration 00035).
	status, _, body := e.get3(t, e.owner, "/projects/p/components/c/metrics/gitlab")
	if status != http.StatusOK || !strings.Contains(body, `href="/projects/p/components/c/metrics/gitlab/runs"`) {
		t.Fatalf("metric page by slugs and kind = %d", status)
	}
	// The breadcrumb passes through the project's GitLab page: a
	// component's page for a kind sits under the project's.
	for _, want := range []string{`href="/projects/p"`, `href="/projects/p/components/c/bind"`, `href="/projects/p/metrics/gitlab"`} {
		if !strings.Contains(body, want) {
			t.Errorf("metric page missing %s", want)
		}
	}
	// The metric page and its run history draw the component's strip, its
	// kind open; the kinds it has not bound are drawn but are not links.
	assertTabs(t, body, componentTabWants("/projects/p/components/c", "gitlab"), "gitlab")
	// Under it, the binding's two views: Overview open here, Syncs on /runs.
	if !strings.Contains(body, `href="/projects/p/components/c/metrics/gitlab" class="lotus-seg-item is-active" aria-current="page">Overview`) {
		t.Error("metric page does not open on its Overview view")
	}
	_, _, body = e.get3(t, e.owner, "/projects/p/components/c/metrics/gitlab/runs")
	assertTabs(t, body, componentTabWants("/projects/p/components/c", "gitlab"), "gitlab")
	if !strings.Contains(body, `href="/projects/p/components/c/metrics/gitlab/runs" class="lotus-seg-item is-active" aria-current="page">Syncs`) {
		t.Error("run history does not open on its Syncs view")
	}

	moves := map[string]string{
		// The retired addresses, as bookmarks and old mail carry them.
		"/components/" + componentID:                                                      "/projects/p/components/c",
		"/components/" + componentID + "/bind?kind=sentry":                                "/projects/p/components/c/bind?kind=sentry",
		"/components/" + componentID + "/sparkline":                                       "/projects/p/components/c/sparkline",
		"/p/" + projectID + "/c/" + componentID + "/m/" + bindingID:                       "/projects/p/components/c/metrics/gitlab",
		"/p/" + projectID + "/c/" + componentID + "/m/" + bindingID + "/runs":             "/projects/p/components/c/metrics/gitlab/runs",
		"/p/" + projectID + "/c/" + componentID + "/m/" + bindingID + "/charts?range=30d": "/projects/p/components/c/metrics/gitlab/charts?range=30d",
		// Ids under the new shape, in any position — the binding's id in
		// place of the kind included — move in one hop.
		"/projects/" + projectID + "/components/" + componentID:                           "/projects/p/components/c",
		"/projects/p/components/" + componentID + "/bind":                                 "/projects/p/components/c/bind",
		"/projects/" + projectID + "/components/c/metrics/gitlab":                         "/projects/p/components/c/metrics/gitlab",
		"/projects/p/components/c/metrics/" + bindingID:                                   "/projects/p/components/c/metrics/gitlab",
		"/projects/p/components/c/metrics/" + bindingID + "/runs":                         "/projects/p/components/c/metrics/gitlab/runs",
		"/projects/p/components/c/metrics/" + bindingID + "/charts?range=30d":             "/projects/p/components/c/metrics/gitlab/charts?range=30d",
		"/projects/" + projectID + "/components/" + componentID + "/metrics/" + bindingID: "/projects/p/components/c/metrics/gitlab",
	}
	for from, to := range moves {
		status, loc, _ := e.get3(t, e.owner, from)
		if status != http.StatusMovedPermanently || loc != to {
			t.Errorf("GET %s = %d → %q; want 301 → %q", from, status, loc, to)
		}
	}
	// A component with nothing bound has no tab to open: its page draws the
	// strip, every kind unbound, and the way to bind one.
	bare := e.component(t, uuid.MustParse(projectID), "bare")
	status, _, body = e.get3(t, e.owner, "/projects/p/components/"+bare.Slug)
	if status != http.StatusOK || !strings.Contains(body, "No metrics bound yet") || !strings.Contains(body, `href="/projects/p/components/bare/bind"`) {
		t.Fatalf("component with nothing bound = %d", status)
	}
	assertTabs(t, body, componentTabWants("/projects/p/components/bare"), "")
	// A component is looked up within its project: another project's, by
	// slug or by id, is 404 — as is a binding under the wrong component, a
	// kind the component has not bound, and a kind that does not exist.
	for _, path := range []string{
		"/projects/p/components/stranger",
		"/projects/p/components/" + stranger.ID.String(),
		"/projects/other/components/c/metrics/" + bindingID,
		"/projects/other/components/c/metrics/gitlab",
		"/projects/p/components/stranger/metrics/" + bindingID,
		"/projects/p/components/c/metrics/sentry",
		"/projects/p/components/c/metrics/k6",
		"/projects/p/components/nope",
	} {
		if status, _, _ := e.get3(t, e.owner, path); status != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", path, status)
		}
	}
}

func TestProjectRef_PostByIDStillWorks(t *testing.T) {
	e := newQAEnv(t, "acme-ref-post")
	p := e.project(t, "portal")
	// A form loaded before slug URLs posts to the id address: it is taken,
	// and what follows is the slug URL.
	resp, _ := e.post(t, e.owner, "/projects/"+p.String()+"/qa/sections", url.Values{"name": {"Auth"}}, false)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/projects/portal/qa/cases?section=") {
		t.Fatalf("post by id = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestProjectRef_SlugsAreEscapedAndUnroutableOnesFallBackToTheID(t *testing.T) {
	e := newQAEnv(t, "acme-ref-esc")
	// Inserted directly: these are slugs the forms no longer accept, or
	// that need escaping.
	var ids [3]uuid.UUID
	withOrgTx(t, e.db, e.orgID, func(tx *sqlx.Tx) {
		for i, slug := range []string{"a b/ü?", "new", "0123456789abcdef0123456789abcdef"} {
			if err := tx.Get(&ids[i], `INSERT INTO projects (org_id, slug, name) VALUES ($1,$2,$2) RETURNING id`, e.orgID, slug); err != nil {
				t.Fatal(err)
			}
		}
	})

	// A slug needing escapes is escaped in links and found by them.
	escaped := "/projects/a%20b%2F%C3%BC%3F"
	if got := e.projectPath(t, ids[0]); got != escaped {
		t.Fatalf("link = %q, want %q", got, escaped)
	}
	if status, _, body := e.get3(t, e.owner, escaped+"/qa"); status != http.StatusOK || !strings.Contains(body, escaped+"/qa/cases") {
		t.Fatalf("escaped slug = %d", status)
	}
	if status, loc, _ := e.get3(t, e.owner, "/projects/"+ids[0].String()); status != http.StatusMovedPermanently || loc != escaped {
		t.Fatalf("by id = %d → %q, want 301 → %q", status, loc, escaped)
	}

	// "new" predates the refusal: /projects/new is the create form, so the
	// project keeps its id URL, and that URL does not redirect to itself.
	if got := e.projectPath(t, ids[1]); got != "/projects/"+ids[1].String() {
		t.Fatalf("reserved slug link = %q, want the id", got)
	}
	if status, _, _ := e.get3(t, e.owner, "/projects/"+ids[1].String()+"/qa"); status != http.StatusOK {
		t.Fatalf("reserved slug's id URL = %d, want 200", status)
	}

	// 32 hex digits are a slug, not an id.
	if status, _, _ := e.get3(t, e.owner, "/projects/0123456789abcdef0123456789abcdef/qa"); status != http.StatusOK {
		t.Fatalf("hex slug = %d, want 200", status)
	}
}

func TestProjectRef_ReservedSlugsAreRefused(t *testing.T) {
	e := newQAEnv(t, "acme-ref-reserved")
	for _, slug := range []string{"new", "move", "..", uuid.NewString()} {
		resp, body := e.post(t, e.owner, "/projects/new", url.Values{"name": {"X"}, "slug": {slug}}, false)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "slug") {
			t.Errorf("new project %q = %d; want 422", slug, resp.StatusCode)
		}
	}
	e.project(t, "portal")
	resp, _ := e.post(t, e.owner, "/projects/portal/components/new", url.Values{"name": {"X"}, "slug": {"new"}, "kind": {"backend"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("new component \"new\" = %d; want 422", resp.StatusCode)
	}
	// What is not reserved for a component is fine for one.
	resp, _ = e.post(t, e.owner, "/projects/portal/components/new", url.Values{"name": {"X"}, "slug": {"move"}, "kind": {"backend"}}, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/portal/components/move" {
		t.Errorf("new component \"move\" = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
