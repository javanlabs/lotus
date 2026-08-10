package authn_test

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

func newSessionTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/protected", authn.RequireTenantSession(deps), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})
	app.Post("/protected", authn.RequireTenantSession(deps), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})
	return app, db, cfg
}

func seedTenantSession(t *testing.T, db *sqlx.DB, orgID, userID uuid.UUID, permEpoch int) string {
	t.Helper()
	raw := "test-tenant-session-" + userID.String()
	_, err := db.Exec(`
		INSERT INTO sessions (id, user_id, org_id, host, perm_epoch, expires_at)
		VALUES ($1, $2, $3, 'acme.test', $4, now() + interval '1 day')`,
		sha256HexForTest(raw), userID, orgID, permEpoch)
	if err != nil {
		t.Fatalf("insert tenant session: %v", err)
	}
	return raw
}

func TestRequireTenantSession_RoleChangeRotatesAndSucceeds(t *testing.T) {
	app, db, cfg := newSessionTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme")
	raw := seedTenantSession(t, db, orgID, userID, 0)
	addSecondOwner(t, db, orgID) // so demoting userID doesn't trip the >=1-Owner trigger

	memberRoleID := mustRoleIDForTest(t, db, "member")
	if err := global.UpdateMembershipRole(t.Context(), db, orgID, userID, memberRoleID); err != nil {
		t.Fatalf("update role: %v", err)
	}

	req := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/protected")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (role change should not require re-login)", resp.StatusCode)
	}

	var newCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "lotus_session" {
			newCookie = c
		}
	}
	if newCookie == nil || newCookie.Value == raw {
		t.Fatal("expected a rotated (different) session cookie after a role change")
	}

	// The old session id must now be revoked: a second request with it fails.
	req2 := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/protected")
	req2.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp2, _ := app.Test(req2)
	if resp2.StatusCode == http.StatusOK {
		t.Fatal("expected old session id to be revoked after rotation")
	}

	// The new session id works.
	req3 := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/protected")
	req3.AddCookie(newCookie)
	resp3, _ := app.Test(req3)
	if resp3.StatusCode != http.StatusOK {
		t.Fatal("expected the new rotated session id to work")
	}
}

func TestRequireTenantSession_RemovedMembershipHardRejected(t *testing.T) {
	app, db, cfg := newSessionTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme")
	raw := seedTenantSession(t, db, orgID, userID, 0)
	addSecondOwner(t, db, orgID) // so removing userID doesn't trip the >=1-Owner trigger

	if err := global.RemoveMembership(t.Context(), db, orgID, userID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	req := hostReq(http.MethodPost, "acme."+cfg.BaseDomain, "/protected")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected removed membership to hard-reject the still-alive session")
	}

	var stillLive bool
	if err := db.Get(&stillLive, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND revoked_at IS NULL)`, sha256HexForTest(raw)); err != nil {
		t.Fatalf("check session: %v", err)
	}
	if stillLive {
		t.Fatal("expected the session row itself to be revoked, not merely inaccessible")
	}
}

func TestRequireTenantSession_UnchangedPermEpochJustTouchesLastSeen(t *testing.T) {
	app, db, cfg := newSessionTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme")
	raw := seedTenantSession(t, db, orgID, userID, 0)

	req := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/protected")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "lotus_session" {
			t.Fatal("expected no cookie rotation when perm_epoch hasn't changed")
		}
	}
}

func TestRequireTenantSession_MissingSessionRedirectsToHandoffOnGET(t *testing.T) {
	app, db, cfg := newSessionTestApp(t)
	if _, err := db.Exec(`INSERT INTO organizations (subdomain, name) VALUES ('acme', 'acme')`); err != nil {
		t.Fatalf("insert org: %v", err)
	}

	req := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/protected")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 (redirect to handoff)", resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("no redirect location: %v", err)
	}
	if loc.Path != "/handoff/start" {
		t.Fatalf("redirected to %q, want /handoff/start", loc.Path)
	}
}

func TestRequireTenantSession_MissingSessionOnPOSTGets401(t *testing.T) {
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}
	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Post("/protected", authn.RequireTenantSession(deps), func(c *fiber.Ctx) error { return c.SendString("ok") })

	if _, err := db.Exec(`INSERT INTO organizations (subdomain, name) VALUES ('acme', 'acme')`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	req := hostReq(http.MethodPost, "acme."+cfg.BaseDomain, "/protected")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSession_RawCookieValueNeverStored(t *testing.T) {
	db := dbtest.Open(t)
	orgID, userID := seedActiveMember(t, db, "acme")
	raw := seedTenantSession(t, db, orgID, userID, 0)

	var count int
	if err := db.Get(&count, `SELECT count(*) FROM sessions WHERE id = $1`, raw); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatal("raw cookie value must never appear as a session id")
	}
}

func TestLogout_RevokesSessionAndClearsCookie(t *testing.T) {
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}
	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Post("/logout", authn.Logout(deps))

	orgID, userID := seedActiveMember(t, db, "acme")
	raw := seedTenantSession(t, db, orgID, userID, 0)

	req := hostReq(http.MethodPost, "acme."+cfg.BaseDomain, "/logout")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}

	var stillLive bool
	if err := db.Get(&stillLive, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND revoked_at IS NULL)`, sha256HexForTest(raw)); err != nil {
		t.Fatalf("check session: %v", err)
	}
	if stillLive {
		t.Fatal("expected logout to revoke the session")
	}
}

// addSecondOwner inserts a second Owner into orgID so a test can demote or
// remove the first without tripping the database's >=1-Owner trigger.
func addSecondOwner(t *testing.T, db *sqlx.DB, orgID uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, orgID.String()+"-owner2@example.com"); err != nil {
		t.Fatalf("insert second owner user: %v", err)
	}
	ownerRoleID := mustRoleIDForTest(t, db, "owner")
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, ownerRoleID); err != nil {
		t.Fatalf("insert second owner membership: %v", err)
	}
}

func mustRoleIDForTest(t *testing.T, db *sqlx.DB, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Get(&id, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, key); err != nil {
		t.Fatalf("lookup role %s: %v", key, err)
	}
	return id
}
