package authz_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/tenant"
)

const middlewareTestDomain = "lotus.test"

func newAuthzTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: middlewareTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	authDeps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/sync",
		authn.RequireTenantSession(authDeps), authz.LoadPrincipal(db), authz.Require("sync.trigger"),
		func(c *fiber.Ctx) error { return c.SendString("synced") })
	app.Get("/settings/danger",
		authn.RequireTenantSession(authDeps), authz.LoadPrincipal(db), authz.Require("org.delete"),
		func(c *fiber.Ctx) error { return c.SendString("deleted") })
	return app, db, cfg
}

func seedMemberWithRole(t *testing.T, db *sqlx.DB, subdomain, roleKey string) (orgID, userID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, subdomain+roleKey+"@example.com"); err != nil {
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

func seedTenantSessionFor(t *testing.T, db *sqlx.DB, orgID, userID uuid.UUID) string {
	t.Helper()
	raw := "sess-" + userID.String()
	_, err := db.Exec(`
		INSERT INTO sessions (id, user_id, org_id, host, perm_epoch, expires_at)
		VALUES ($1, $2, $3, 'acme.test', 0, now() + interval '1 day')`,
		sha256HexForAuthzTest(raw), userID, orgID)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return raw
}

func TestRequire_MemberCanTriggerSync(t *testing.T) {
	app, db, cfg := newAuthzTestApp(t)
	orgID, userID := seedMemberWithRole(t, db, "acme", "member")
	raw := seedTenantSessionFor(t, db, orgID, userID)

	req := httptest.NewRequest(http.MethodGet, "http://acme."+cfg.BaseDomain+"/sync", nil)
	req.Host = "acme." + cfg.BaseDomain
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Member sync.trigger status = %d, want 200", resp.StatusCode)
	}
}

func TestRequire_ViewerCannotTriggerSync(t *testing.T) {
	app, db, cfg := newAuthzTestApp(t)
	orgID, userID := seedMemberWithRole(t, db, "acme", "viewer")
	raw := seedTenantSessionFor(t, db, orgID, userID)

	req := httptest.NewRequest(http.MethodGet, "http://acme."+cfg.BaseDomain+"/sync", nil)
	req.Host = "acme." + cfg.BaseDomain
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Viewer sync.trigger status = %d, want 403", resp.StatusCode)
	}
}

func TestRequire_AdminCannotDeleteOrg(t *testing.T) {
	app, db, cfg := newAuthzTestApp(t)
	orgID, userID := seedMemberWithRole(t, db, "acme", "admin")
	raw := seedTenantSessionFor(t, db, orgID, userID)

	req := httptest.NewRequest(http.MethodGet, "http://acme."+cfg.BaseDomain+"/settings/danger", nil)
	req.Host = "acme." + cfg.BaseDomain
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Admin org.delete status = %d, want 403", resp.StatusCode)
	}
}

func TestRequire_OwnerCanDeleteOrg(t *testing.T) {
	app, db, cfg := newAuthzTestApp(t)
	orgID, userID := seedMemberWithRole(t, db, "acme", "owner")
	raw := seedTenantSessionFor(t, db, orgID, userID)

	req := httptest.NewRequest(http.MethodGet, "http://acme."+cfg.BaseDomain+"/settings/danger", nil)
	req.Host = "acme." + cfg.BaseDomain
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Owner org.delete status = %d, want 200", resp.StatusCode)
	}
}

func TestCheckOwnership(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	if err := authz.CheckOwnership(orgA, orgA); err != nil {
		t.Errorf("matching org should pass: %v", err)
	}
	if err := authz.CheckOwnership(orgB, orgA); err == nil {
		t.Fatal("mismatched org should be rejected")
	} else if fe, ok := err.(*fiber.Error); !ok || fe.Code != fiber.StatusNotFound {
		t.Errorf("mismatched org error = %v, want 404 (not 403 — must not confirm existence)", err)
	}
}

func sha256HexForAuthzTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A guest is told a page outside QA does not exist (404); inside QA a
// missing permission is still 403, and everyone else keeps 403 throughout.
func TestRequire_GuestOutsideQAIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		role, perm string
		want       int
	}{
		{"guest", "dashboard.read", http.StatusNotFound},
		{"guest", "qa.case.write", http.StatusForbidden},
		{"guest", "qa.execute", http.StatusOK},
		{"viewer", "sync.trigger", http.StatusForbidden},
	} {
		app := fiber.New()
		app.Get("/x", func(c *fiber.Ctx) error {
			authz.SetPrincipal(c, authz.Principal{Kind: authz.KindSession, Role: tc.role})
			return c.Next()
		}, authz.Require(tc.perm), func(c *fiber.Ctx) error { return c.SendString("ok") })
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/x", nil))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("%s on %s = %d, want %d", tc.role, tc.perm, resp.StatusCode, tc.want)
		}
	}
}
