package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaEnv is one organization with an owner session, for the QA screens.
type qaEnv struct {
	app   *fiber.App
	db    *sqlx.DB
	orgID uuid.UUID
	s     tenant.Scope
	host  string
	owner string // owner session cookie
}

func newQAEnv(t *testing.T, sub string) qaEnv {
	t.Helper()
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, sub, "owner")
	return qaEnv{
		app: app, db: db, orgID: orgID, s: tenant.FromRun(db, orgID),
		host: sub + "." + dashboardTestDomain, owner: seedSession(t, db, orgID, ownerID),
	}
}

// session adds a user with roleKey to the organization and returns its
// session cookie and user id.
func (e qaEnv) session(t *testing.T, email, roleKey string) (string, uuid.UUID) {
	t.Helper()
	var userID, roleID uuid.UUID
	if err := e.db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, email); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := e.db.Get(&roleID, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, roleKey); err != nil {
		t.Fatalf("lookup role %s: %v", roleKey, err)
	}
	if _, err := e.db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, e.orgID, userID, roleID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return seedSession(t, e.db, e.orgID, userID), userID
}

func (e qaEnv) project(t *testing.T, name string) uuid.UUID {
	t.Helper()
	p, err := repo.CreateProject(context.Background(), e.s, repo.NewProject{Slug: url.PathEscape(name), Name: name})
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	return p.ID
}

// get fetches path with the given session and returns status and body.
func (e qaEnv) get(t *testing.T, raw, path string) (int, string) {
	t.Helper()
	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, path, raw))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// post submits a form (htmx when hx is true) and returns the response.
func (e qaEnv) post(t *testing.T, raw, path string, form url.Values, hx bool) (*http.Response, string) {
	t.Helper()
	resp, err := e.app.Test(folderFormReq(e.host, path, raw, form, hx), 30_000)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// hasText reports whether body shows text, allowing for html/template's
// escaping of apostrophes.
func hasText(body, text string) bool {
	return strings.Contains(body, text) || strings.Contains(body, strings.ReplaceAll(text, "'", "&#39;"))
}
