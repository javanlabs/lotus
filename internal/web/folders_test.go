package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

func folderFormReq(host, path, raw string, form url.Values, htmx bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	return req
}

func folderIDNamed(t *testing.T, db *sqlx.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `SELECT id FROM folders WHERE name = $1`, name); err != nil {
			t.Fatalf("find folder %q: %v", name, err)
		}
	})
	return id
}

func auditCount(t *testing.T, db *sqlx.DB, orgID uuid.UUID, action string) int {
	t.Helper()
	var n int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&n, `SELECT count(*) FROM audit_events WHERE action = $1`, action); err != nil {
			t.Fatalf("count audit %s: %v", action, err)
		}
	})
	return n
}

func TestFolders_MemberCreatesNestedFoldersAndItIsAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-fold", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-fold." + dashboardTestDomain

	resp, err := app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"IT Services"}}, false))
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects" {
		t.Fatalf("create = %v, %v; want 302 to /projects", resp, err)
	}
	it := folderIDNamed(t, db, orgID, "IT Services")

	resp, err = app.Test(folderFormReq(host, "/folders", raw,
		url.Values{"name": {"Tribe lead A"}, "parent_id": {it.String()}}, true))
	if err != nil || resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Refresh") != "true" {
		t.Fatalf("htmx create = %v, %v; want 204 with HX-Refresh", resp, err)
	}
	folderIDNamed(t, db, orgID, "Tribe lead A")
	if n := auditCount(t, db, orgID, "folder.create"); n != 2 {
		t.Fatalf("folder.create audit events = %d, want 2", n)
	}
}

func TestFolders_ViewerCannotCreate_MemberCannotDelete(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, viewerID := seedMember(t, db, "acme-perm", "viewer")
	viewerRaw := seedSession(t, db, orgID, viewerID)
	host := "acme-perm." + dashboardTestDomain

	resp, _ := app.Test(folderFormReq(host, "/folders", viewerRaw, url.Values{"name": {"X"}}, false))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create status = %d, want 403", resp.StatusCode)
	}

	var memberID uuid.UUID
	if err := db.Get(&memberID, `INSERT INTO users (email, email_verified_at) VALUES ('m@acme-perm.test', now()) RETURNING id`); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE key = 'member' AND org_id IS NULL`, orgID, memberID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	memberRaw := seedSession(t, db, orgID, memberID)
	app.Test(folderFormReq(host, "/folders", memberRaw, url.Values{"name": {"Old"}}, false))
	id := folderIDNamed(t, db, orgID, "Old")

	resp, _ = app.Test(folderFormReq(host, "/folders/"+id.String()+"/delete", memberRaw, nil, false))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete status = %d, want 403", resp.StatusCode)
	}
}

// Review focus (final review, item 6): spec §9 wants "a viewer refused on
// every write" — the existing test only covered create. This adds rename,
// move, delete and project move.
func TestFolders_ViewerRefusedOnEveryWrite(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-viewer-w", "owner")
	ownerRaw := seedSession(t, db, orgID, ownerID)
	host := "acme-viewer-w." + dashboardTestDomain

	app.Test(folderFormReq(host, "/folders", ownerRaw, url.Values{"name": {"Product"}}, false))
	product := folderIDNamed(t, db, orgID, "Product")
	app.Test(folderFormReq(host, "/folders", ownerRaw, url.Values{"name": {"Other"}}, false))
	other := folderIDNamed(t, db, orgID, "Other")
	var projectID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'v','V') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	var viewerID uuid.UUID
	if err := db.Get(&viewerID, `INSERT INTO users (email, email_verified_at) VALUES ('viewer@acme-viewer-w.test', now()) RETURNING id`); err != nil {
		t.Fatalf("insert viewer: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE key = 'viewer' AND org_id IS NULL`, orgID, viewerID); err != nil {
		t.Fatalf("membership: %v", err)
	}
	viewerRaw := seedSession(t, db, orgID, viewerID)

	for _, step := range []struct {
		name, path string
		form       url.Values
	}{
		{"rename", "/folders/" + product.String() + "/rename", url.Values{"name": {"Renamed"}}},
		{"move", "/folders/" + product.String() + "/move", url.Values{"parent_id": {other.String()}}},
		{"delete", "/folders/" + product.String() + "/delete", nil},
		{"project move", "/projects/" + projectID.String() + "/move", url.Values{"folder_id": {product.String()}}},
		{"bulk project move", "/projects/move", url.Values{"project_id": {projectID.String()}, "folder_id": {product.String()}}},
	} {
		t.Run(step.name, func(t *testing.T) {
			resp, err := app.Test(folderFormReq(host, step.path, viewerRaw, step.form, false))
			if err != nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("viewer %s status = %v, %v; want 403", step.name, resp, err)
			}
		})
	}
}

func TestFolders_RejectionsComeBackIntoTheDialogForHtmx(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-rej", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-rej." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Product"}}, false))
	product := folderIDNamed(t, db, orgID, "Product")
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Inner"}, "parent_id": {product.String()}}, false))
	inner := folderIDNamed(t, db, orgID, "Inner")

	cases := []struct {
		name, path string
		form       url.Values
		wantStatus int
		wantText   string
	}{
		{"duplicate name", "/folders", url.Values{"name": {"product"}}, http.StatusConflict,
			"There's already a folder called 'product' here."},
		{"blank name", "/folders", url.Values{"name": {"  "}}, http.StatusUnprocessableEntity,
			"Folder name is required"},
		{"cycle", "/folders/" + product.String() + "/move", url.Values{"parent_id": {inner.String()}}, http.StatusConflict,
			"Can't move a folder into itself."},
		{"not empty", "/folders/" + product.String() + "/delete", nil, http.StatusConflict,
			"'Product' still contains 1 folder."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := app.Test(folderFormReq(host, c.path, raw, c.form, true))
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("htmx status = %v, %v; want 200", resp, err)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), c.wantText) && !strings.Contains(string(body), strings.ReplaceAll(c.wantText, "'", "&#39;")) {
				t.Fatalf("htmx body = %s, want it to contain %q", body, c.wantText)
			}
			resp, _ = app.Test(folderFormReq(host, c.path, raw, c.form, false))
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("plain status = %d, want %d", resp.StatusCode, c.wantStatus)
			}
		})
	}
}

// Review focus (final review, item 3): the pre-fetch repo.FolderByID in
// rename/move used to return writeRepoErr's plain JSON 404, which htmx does
// not swap into the dialog — the button just did nothing. It now goes
// through folderFormError like every other rejection, so the dialog shows
// "That folder no longer exists."
func TestFolders_RenameMoveOfAMissingFolderShowsInTheDialog(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-gone", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-gone." + dashboardTestDomain
	missing := uuid.New()

	for _, path := range []string{
		"/folders/" + missing.String() + "/rename",
		"/folders/" + missing.String() + "/move",
	} {
		resp, err := app.Test(folderFormReq(host, path, raw, url.Values{"name": {"X"}}, true))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s htmx status = %v, %v; want 200", path, resp, err)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "That folder no longer exists.") {
			t.Fatalf("%s htmx body = %s, want it to contain the not-found message", path, body)
		}
	}
}

func TestFolders_RenameMoveDeleteAreAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-aud", "admin")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-aud." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"IT"}}, false))
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Tribe"}}, false))
	it := folderIDNamed(t, db, orgID, "IT")
	tribe := folderIDNamed(t, db, orgID, "Tribe")

	for _, step := range []struct {
		path string
		form url.Values
	}{
		{"/folders/" + it.String() + "/rename", url.Values{"name": {"IT Services"}}},
		{"/folders/" + tribe.String() + "/move", url.Values{"parent_id": {it.String()}}},
		{"/folders/" + tribe.String() + "/move", url.Values{"parent_id": {""}}},
		{"/folders/" + tribe.String() + "/delete", nil},
	} {
		resp, err := app.Test(folderFormReq(host, step.path, raw, step.form, false))
		if err != nil || resp.StatusCode != http.StatusFound {
			t.Fatalf("%s = %v, %v; want 302", step.path, resp, err)
		}
	}
	for action, want := range map[string]int{"folder.rename": 1, "folder.move": 2, "folder.delete": 1} {
		if n := auditCount(t, db, orgID, action); n != want {
			t.Fatalf("%s audit events = %d, want %d", action, n, want)
		}
	}
}

func TestProjects_MoveIntoAFolderIsAudited(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-mv", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-mv." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"Product"}}, false))
	product := folderIDNamed(t, db, orgID, "Product")
	var projectID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'syarq','SyarQ') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	resp, err := app.Test(folderFormReq(host, "/projects/"+projectID.String()+"/move", raw,
		url.Values{"folder_id": {product.String()}}, false))
	if err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/"+projectID.String() {
		t.Fatalf("move = %v, %v; want 302 back to the project", resp, err)
	}
	var folderID *uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&folderID, `SELECT folder_id FROM projects WHERE id = $1`, projectID); err != nil {
			t.Fatalf("read project: %v", err)
		}
	})
	if folderID == nil || *folderID != product {
		t.Fatalf("folder_id = %v, want %v", folderID, product)
	}
	if n := auditCount(t, db, orgID, "project.move"); n != 1 {
		t.Fatalf("project.move audit events = %d, want 1", n)
	}
}

func seedProjectsNamed(t *testing.T, db *sqlx.DB, orgID uuid.UUID, names ...string) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, len(names))
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		for i, n := range names {
			if err := tx.Get(&ids[i], `INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, orgID, n); err != nil {
				t.Fatalf("insert project %s: %v", n, err)
			}
		}
	})
	return ids
}

func TestProjects_BulkMoveFilesEveryProjectAndAuditsEach(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-bulk", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-bulk." + dashboardTestDomain
	app.Test(folderFormReq(host, "/folders", raw, url.Values{"name": {"IT Services"}}, false))
	it := folderIDNamed(t, db, orgID, "IT Services")
	ids := seedProjectsNamed(t, db, orgID, "linknet", "dignas", "anri")

	resp, err := app.Test(folderFormReq(host, "/projects/move", raw,
		url.Values{"project_id": {ids[0].String(), ids[1].String()}, "folder_id": {it.String()}}, true))
	if err != nil || resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Refresh") != "true" {
		t.Fatalf("bulk move = %v, %v; want 204 with HX-Refresh", resp, err)
	}
	var filed int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&filed, `SELECT count(*) FROM projects WHERE folder_id = $1`, it); err != nil {
			t.Fatalf("count: %v", err)
		}
	})
	if filed != 2 {
		t.Fatalf("%d projects filed in IT Services, want 2 (anri must stay put)", filed)
	}
	if n := auditCount(t, db, orgID, "project.move"); n != 2 {
		t.Fatalf("project.move audit events = %d, want 2", n)
	}
}

func TestProjects_BulkMoveRejectionsComeBackIntoTheDialog(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-bulk-rej", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-bulk-rej." + dashboardTestDomain
	ids := seedProjectsNamed(t, db, orgID, "linknet")

	for name, c := range map[string]struct {
		form url.Values
		want string
	}{
		"nothing selected": {url.Values{"folder_id": {""}}, "Select at least one project."},
		"unknown folder":   {url.Values{"project_id": {ids[0].String()}, "folder_id": {uuid.NewString()}}, "That project or folder no longer exists."},
		"unknown project":  {url.Values{"project_id": {uuid.NewString()}, "folder_id": {""}}, "That project or folder no longer exists."},
		"malformed id":     {url.Values{"project_id": {"nope"}, "folder_id": {""}}, "That project or folder no longer exists."},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := app.Test(folderFormReq(host, "/projects/move", raw, c.form, true))
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %v, %v; want 200 fragment", resp, err)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), c.want) {
				t.Fatalf("body = %s, want %q", body, c.want)
			}
		})
	}
}
