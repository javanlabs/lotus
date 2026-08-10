package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func sha256HexForProjectsTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const projectsTestDomain = "lotus.test"

func newProjectsTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: projectsTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	registerProjectRoutes(app, authn.RequireTenantSession(deps), authz.LoadPrincipal(db))
	return app, db, cfg
}

// seedMember creates an org, a user, and a membership with roleKey,
// returning both ids — same shape as internal/authn's seedActiveMember but
// with a caller-chosen role, since these tests need non-owner principals
// to exercise permission boundaries.
func seedMember(t *testing.T, db *sqlx.DB, subdomain, roleKey string) (orgID, userID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, subdomain+"-"+roleKey+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var roleID uuid.UUID
	if err := db.Get(&roleID, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, roleKey); err != nil {
		t.Fatalf("lookup role %s: %v", roleKey, err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, roleID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return orgID, userID
}

func seedSession(t *testing.T, db *sqlx.DB, orgID, userID uuid.UUID) string {
	t.Helper()
	raw := "test-session-" + userID.String()
	_, err := db.Exec(`
		INSERT INTO sessions (id, user_id, org_id, host, expires_at)
		VALUES ($1, $2, $3, 'acme.test', now() + interval '1 day')`,
		sha256HexForProjectsTest(raw), userID, orgID)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return raw
}

func reqWithSession(method, host, path, raw string) *http.Request {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	return req
}

func jsonReqWithSession(method, host, path, raw, body string) *http.Request {
	req := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	return req
}

func TestProjects_CreateThenListThenGet(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	_ = orgID
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	createResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/projects", raw,
		`{"slug":"web","name":"Web","description":"desc"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", createResp.StatusCode)
	}
	var created repo.Project
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects", raw))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list []repo.Project
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Slug != "web" {
		t.Fatalf("list = %+v, want one project with slug=web", list)
	}

	getResp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects/"+created.ID.String(), raw))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", getResp.StatusCode)
	}

	// Component under the project.
	compResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/projects/"+created.ID.String()+"/components", raw,
		`{"slug":"api","name":"API","kind":"backend"}`))
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	if compResp.StatusCode != http.StatusCreated {
		t.Fatalf("create component status = %d, want 201", compResp.StatusCode)
	}
	var comp repo.Component
	if err := json.NewDecoder(compResp.Body).Decode(&comp); err != nil {
		t.Fatalf("decode component: %v", err)
	}
	if comp.ProjectID != created.ID {
		t.Fatalf("component project_id = %v, want %v", comp.ProjectID, created.ID)
	}
}

func TestProjects_SlugUniquePerOrgNotGlobal(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgA, userA := seedMember(t, db, "acme", "owner")
	orgB, userB := seedMember(t, db, "beta", "owner")
	rawA := seedSession(t, db, orgA, userA)
	rawB := seedSession(t, db, orgB, userB)

	// Same slug in org A twice: second is rejected.
	hostA := "acme." + cfg.BaseDomain
	resp1, err := app.Test(jsonReqWithSession(http.MethodPost, hostA, "/projects", rawA, `{"slug":"web","name":"Web"}`))
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("create 1 status = %d, want 201", resp1.StatusCode)
	}
	resp2, err := app.Test(jsonReqWithSession(http.MethodPost, hostA, "/projects", rawA, `{"slug":"web","name":"Web Again"}`))
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("create 2 (same org, same slug) status = %d, want 409", resp2.StatusCode)
	}

	// Same slug in org B: allowed.
	hostB := "beta." + cfg.BaseDomain
	resp3, err := app.Test(jsonReqWithSession(http.MethodPost, hostB, "/projects", rawB, `{"slug":"web","name":"Web"}`))
	if err != nil {
		t.Fatalf("create 3: %v", err)
	}
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("create 3 (different org, same slug) status = %d, want 201", resp3.StatusCode)
	}
}

func TestComponents_RenameDoesNotBreakBindings(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgID, "web")
	componentID := mustSeedComponent(t, db, orgID, projectID, "api")
	connID := mustSeedConnection(t, db, orgID)
	bindingID := mustSeedBinding(t, db, orgID, componentID, connID)

	resp, err := app.Test(jsonReqWithSession(http.MethodPatch, host, "/components/"+componentID.String(), raw,
		`{"slug":"api-v2","name":"API v2","sort_order":1}`))
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d, want 200", resp.StatusCode)
	}

	var stillLinked bool
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&stillLinked, `SELECT EXISTS(SELECT 1 FROM metric_bindings WHERE id = $1 AND component_id = $2)`, bindingID, componentID); err != nil {
			t.Fatalf("check binding: %v", err)
		}
	})
	if !stillLinked {
		t.Fatal("renaming a component must not break its metric binding")
	}
}

func TestProjects_CrossOrgAccessIs404NotForbidden(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgA, _ := seedMember(t, db, "acme", "owner")
	orgB, userB := seedMember(t, db, "beta", "owner")
	rawB := seedSession(t, db, orgB, userB)

	projectInA := mustSeedProject(t, db, orgA, "secret")

	resp, err := app.Test(reqWithSession(http.MethodGet, "beta."+cfg.BaseDomain, "/projects/"+projectInA.String(), rawB))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org get status = %d, want 404 (not 403)", resp.StatusCode)
	}

	patchResp, err := app.Test(jsonReqWithSession(http.MethodPatch, "beta."+cfg.BaseDomain, "/projects/"+projectInA.String(), rawB,
		`{"slug":"stolen","name":"Stolen"}`))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patchResp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org patch status = %d, want 404 (not 403)", patchResp.StatusCode)
	}
}

func TestProjects_ArchiveHidesButPreservesData(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain
	projectID := mustSeedProject(t, db, orgID, "web")

	archiveResp, err := app.Test(reqWithSession(http.MethodPost, host, "/projects/"+projectID.String()+"/archive", raw))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archiveResp.StatusCode != http.StatusNoContent {
		t.Fatalf("archive status = %d, want 204", archiveResp.StatusCode)
	}

	listResp, _ := app.Test(reqWithSession(http.MethodGet, host, "/projects", raw))
	var list []repo.Project
	json.NewDecoder(listResp.Body).Decode(&list)
	if len(list) != 0 {
		t.Fatalf("default list should hide archived projects, got %d", len(list))
	}

	getResp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects/"+projectID.String(), raw))
	if err != nil {
		t.Fatalf("get archived: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("archived project's own page should still be readable, status = %d", getResp.StatusCode)
	}
}

func TestProjects_ViewerCannotCreate(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "viewer")
	raw := seedSession(t, db, orgID, userID)

	resp, err := app.Test(jsonReqWithSession(http.MethodPost, "acme."+cfg.BaseDomain, "/projects", raw, `{"slug":"web","name":"Web"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create status = %d, want 403", resp.StatusCode)
	}
}

func TestProjects_HardDeleteRequiresMatchingConfirmationCounts(t *testing.T) {
	app, db, cfg := newProjectsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain
	projectID := mustSeedProject(t, db, orgID, "web")
	mustSeedComponent(t, db, orgID, projectID, "api")

	// Stale/wrong confirmation counts are rejected.
	staleResp, err := app.Test(reqWithSession(http.MethodDelete, host, "/projects/"+projectID.String()+"?confirm_components=0&confirm_bindings=0", raw))
	if err != nil {
		t.Fatalf("delete stale: %v", err)
	}
	if staleResp.StatusCode != http.StatusConflict {
		t.Fatalf("delete with stale counts status = %d, want 409", staleResp.StatusCode)
	}

	// Correct counts succeed.
	okResp, err := app.Test(reqWithSession(http.MethodDelete, host, "/projects/"+projectID.String()+"?confirm_components=1&confirm_bindings=0", raw))
	if err != nil {
		t.Fatalf("delete ok: %v", err)
	}
	if okResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete with correct counts status = %d, want 204", okResp.StatusCode)
	}

	var exists bool
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&exists, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1)`, projectID); err != nil {
			t.Fatalf("check project exists: %v", err)
		}
	})
	if exists {
		t.Fatal("project should be gone after confirmed hard delete")
	}
}

// withOrgTx runs fn inside a transaction with app.org_id set for orgID —
// every tenant-plane table is FORCE RLS (task 0.3), so any direct seed
// insert or fixture check against projects/components/... must go through
// this, exactly like tenant.Scope.Tx does for real request handling.
func withOrgTx(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
}

func mustSeedProject(t *testing.T, db *sqlx.DB, orgID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, orgID, slug); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	})
	return id
}

func mustSeedComponent(t *testing.T, db *sqlx.DB, orgID, projectID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $3, 'backend') RETURNING id`, orgID, projectID, slug); err != nil {
			t.Fatalf("seed component: %v", err)
		}
	})
	return id
}

func mustSeedConnection(t *testing.T, db *sqlx.DB, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'gl', 'https://gitlab.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("seed connection: %v", err)
		}
	})
	return id
}

func mustSeedBinding(t *testing.T, db *sqlx.DB, orgID, componentID, connID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'gitlab', 'group/repo') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
	})
	return id
}
