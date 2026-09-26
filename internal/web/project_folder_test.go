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

func TestProjectPage_MoveToFolderIsOfferedToMembersOnly(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{{"member", true}, {"viewer", false}} {
		t.Run(tc.role, func(t *testing.T) {
			app, db := newDashboardTestApp(t)
			sub := "acme-pf-" + tc.role
			orgID, userID := seedMember(t, db, sub, tc.role)
			raw := seedSession(t, db, orgID, userID)
			var projectID uuid.UUID
			withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
				if _, err := tx.Exec(`INSERT INTO folders (org_id, name) VALUES ($1, 'Product')`, orgID); err != nil {
					t.Fatalf("insert folder: %v", err)
				}
				if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'aos','AOS Online') RETURNING id`, orgID); err != nil {
					t.Fatalf("insert project: %v", err)
				}
			})
			resp, err := app.Test(reqWithSession(http.MethodGet, sub+"."+dashboardTestDomain, "/projects/"+projectID.String(), raw))
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("GET project = %v, %v", resp, err)
			}
			body, _ := io.ReadAll(resp.Body)
			if got := strings.Contains(string(body), "Move to folder"); got != tc.want {
				t.Fatalf("Move to folder shown = %v, want %v", got, tc.want)
			}
			if tc.want && !strings.Contains(string(body), "Product") {
				t.Fatal("folder picker does not list Product")
			}
		})
	}
}

func TestNewProject_CanBeFiledInAFolder(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-np", "member")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-np." + dashboardTestDomain
	var folderID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&folderID, `INSERT INTO folders (org_id, name) VALUES ($1, 'Product') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert folder: %v", err)
		}
	})

	getResp, _ := app.Test(reqWithSession(http.MethodGet, host, "/projects/new", raw))
	page, _ := io.ReadAll(getResp.Body)
	if !strings.Contains(string(page), `name="folder_id"`) {
		t.Fatal("new project form has no folder picker")
	}

	form := url.Values{"name": {"SyarQ"}, "slug": {"syarq"}, "folder_id": {folderID.String()}}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/projects/new", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("create = %v, %v; want 302", resp, err)
	}
	var got *uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&got, `SELECT folder_id FROM projects WHERE slug = 'syarq'`); err != nil {
			t.Fatalf("read project: %v", err)
		}
	})
	if got == nil || *got != folderID {
		t.Fatalf("folder_id = %v, want %v", got, folderID)
	}
}
