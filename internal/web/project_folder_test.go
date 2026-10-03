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
			resp, err := app.Test(reqWithSession(http.MethodGet, sub+"."+dashboardTestDomain, projectPathOf(t, db, orgID, projectID), raw))
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

func TestProjectPage_MoveToFolderIsANormalButton(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-pf-btn", "member")
	raw := seedSession(t, db, orgID, userID)
	ids := seedProjectsNamed(t, db, orgID, "aos")
	resp, err := app.Test(reqWithSession(http.MethodGet, "acme-pf-btn."+dashboardTestDomain, projectPathOf(t, db, orgID, ids[0]), raw))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET project = %v, %v", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	i := strings.Index(body, "Move to folder\n")
	if i < 0 {
		t.Fatal("Move to folder trigger not found")
	}
	trigger := body[strings.LastIndex(body[:i], "<button"):i]
	if !strings.Contains(trigger, "btn-lotus-secondary") {
		t.Fatalf("Move to folder trigger = %s, want a secondary button", trigger)
	}
}

func TestProjectPage_BreadcrumbShowsTheFolderPath(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-pf-crumb", "viewer")
	raw := seedSession(t, db, orgID, userID)
	seedFiledProject(t, db, orgID, "IT Services", "Linknet", "pass", 90)
	var projectID, folderID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.QueryRow(`SELECT id, folder_id FROM projects WHERE name = 'Linknet'`).Scan(&projectID, &folderID); err != nil {
			t.Fatalf("find project: %v", err)
		}
	})
	resp, err := app.Test(reqWithSession(http.MethodGet, "acme-pf-crumb."+dashboardTestDomain, projectPathOf(t, db, orgID, projectID), raw))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET project = %v, %v", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if strings.Contains(body, `>Projects</a>`) {
		t.Fatal("breadcrumb still has a separate Projects crumb")
	}
	for _, want := range []string{
		// The organization's name is the link to the top level; there is
		// no separate "Projects" crumb.
		`<a href="/projects" style="color:#6B7280">acme-pf-crumb</a>`,
		`<a href="/projects?folder=` + folderID.String() + `" style="color:#6B7280">IT Services</a>`,
		`aria-current="page" style="color:#43284E;font-weight:600">Linknet</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("breadcrumb missing %q", want)
		}
	}
}
