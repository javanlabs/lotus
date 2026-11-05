package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

// flatListFixture is an org with folder ITSD holding Linknet (tag PROJECT)
// and Dignas at the top level — the shape production had before tags.
type flatListFixture struct {
	db              *sqlx.DB
	orgID, itsd     uuid.UUID
	linknet, dignas uuid.UUID
	host, raw       string
	get             func(path string) (int, string)
	post            func(path string) int
}

func newFlatListFixture(t *testing.T, subdomain string) *flatListFixture {
	t.Helper()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, subdomain, "owner")
	f := &flatListFixture{db: db, orgID: orgID, raw: seedSession(t, db, orgID, userID), host: subdomain + "." + dashboardTestDomain}
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		must := func(dst *uuid.UUID, q string, args ...any) {
			if err := tx.Get(dst, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		must(&f.itsd, `INSERT INTO folders (org_id, name) VALUES ($1, 'ITSD') RETURNING id`, orgID)
		must(&f.linknet, `INSERT INTO projects (org_id, slug, name, folder_id) VALUES ($1, 'linknet', 'Linknet', $2) RETURNING id`, orgID, f.itsd)
		must(&f.dignas, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'dignas', 'Dignas') RETURNING id`, orgID)
		var tag uuid.UUID
		must(&tag, `INSERT INTO tags (org_id, name) VALUES ($1, 'PROJECT') RETURNING id`, orgID)
		if _, err := tx.Exec(`INSERT INTO project_tags (org_id, project_id, tag_id) VALUES ($1, $2, $3)`, orgID, f.linknet, tag); err != nil {
			t.Fatal(err)
		}
	})
	f.get = func(path string) (int, string) {
		t.Helper()
		resp, err := app.Test(reqWithSession(http.MethodGet, f.host, path, f.raw), -1)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	f.post = func(path string) int {
		t.Helper()
		resp, err := app.Test(formPostReq(f.host, path, f.raw, url.Values{"name": {"X"}}, false), -1)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	return f
}

// projectRowHTML is the list row linking to the project, up to the next row.
func projectRowHTML(t *testing.T, body, slug string) string {
	t.Helper()
	at := strings.Index(body, `href="/projects/`+slug+`"`)
	if at < 0 {
		t.Fatalf("no row for %s", slug)
	}
	row := body[at:]
	if end := strings.Index(row[1:], `<a href=`); end > 0 {
		row = row[:end+1]
	}
	return row
}

func TestProjectsPage_IsFlatAndShowsTags(t *testing.T) {
	t.Parallel()
	f := newFlatListFixture(t, "acme-flat")
	status, body := f.get("/projects")
	if status != http.StatusOK {
		t.Fatalf("GET /projects = %d", status)
	}
	for _, want := range []string{`href="/projects/linknet"`, `href="/projects/dignas"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if row := projectRowHTML(t, body, "linknet"); !strings.Contains(row, "PROJECT") {
		t.Errorf("Linknet's row does not show its tag:\n%s", row)
	}
	for _, gone := range []string{"Move to folder", "New folder", "data-folder", `href="/projects?folder=`} {
		if strings.Contains(body, gone) {
			t.Errorf("page still contains %q", gone)
		}
	}
}

// A bookmark from before tags must still open the list, not 404.
func TestProjectsPage_OldFolderLinkStillRenders(t *testing.T) {
	t.Parallel()
	f := newFlatListFixture(t, "acme-oldlink")
	status, body := f.get("/projects?folder=" + f.itsd.String())
	if status != http.StatusOK {
		t.Fatalf("GET /projects?folder= = %d, want 200", status)
	}
	for _, want := range []string{`href="/projects/linknet"`, `href="/projects/dignas"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// A malformed id is ignored too.
	if status, _ := f.get("/projects?folder=nope"); status != http.StatusOK {
		t.Errorf("GET /projects?folder=nope = %d, want 200", status)
	}
}

func TestProjectsPage_FiltersByTag(t *testing.T) {
	t.Parallel()
	f := newFlatListFixture(t, "acme-flatfilter")
	_, body := f.get("/projects?tag=PROJECT")
	if !strings.Contains(body, `href="/projects/linknet"`) {
		t.Fatalf("filtered list misses Linknet")
	}
	if strings.Contains(body, `href="/projects/dignas"`) {
		t.Fatalf("filtered list still shows Dignas")
	}
	if !strings.Contains(body, `data-testid="tag-filter"`) {
		t.Fatalf("no tag filter on the list")
	}
}

func TestFolderRoutesAreGone(t *testing.T) {
	t.Parallel()
	f := newFlatListFixture(t, "acme-nofolders")
	for _, path := range []string{
		"/folders",
		"/folders/" + f.itsd.String() + "/rename",
		"/folders/" + f.itsd.String() + "/delete",
		"/projects/" + f.linknet.String() + "/move",
	} {
		if status := f.post(path); status != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, status)
		}
	}
	// /projects/move now reads as GET /projects/:id with the slug "move",
	// so the router answers the POST 405 rather than 404. Either way
	// nothing handles it.
	if status := f.post("/projects/move"); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Errorf("POST /projects/move = %d, want 404 or 405", status)
	}
}

func TestProjectBreadcrumbHasNoFolder(t *testing.T) {
	t.Parallel()
	f := newFlatListFixture(t, "acme-crumb")
	status, body := f.get("/projects/linknet")
	if status != http.StatusOK {
		t.Fatalf("GET /projects/linknet = %d", status)
	}
	start := strings.Index(body, `aria-label="Breadcrumb"`)
	end := strings.Index(body[start:], "</nav>")
	crumbs := body[start : start+end]
	if strings.Contains(crumbs, "ITSD") || strings.Contains(crumbs, "folder=") {
		t.Fatalf("breadcrumb still names the folder:\n%s", crumbs)
	}
	if strings.Contains(body, "Move to folder") {
		t.Fatalf("project page still offers Move to folder")
	}
	trail := projectTrail(&repo.Project{ID: f.linknet, Slug: "linknet", Name: "Linknet"}, trailMenus{})
	if len(trail) != 1 || trail[0].Label != "Linknet" {
		t.Fatalf("project trail = %+v, want just the project after the organization", trail)
	}
}
