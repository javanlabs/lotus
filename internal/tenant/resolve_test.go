package tenant_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/tenant"
)

const testBaseDomain = "lotus.test"

func newTestApp(t *testing.T, dev bool) (*fiber.App, *sqlx.DB) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: testBaseDomain, Dev: dev}
	resolver := tenant.NewResolver(cfg, db)

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/tenant-only", tenant.RequireTenant(), func(c *fiber.Ctx) error {
		org, _ := tenant.OrgFromContext(c)
		return c.SendString("tenant:" + org.Subdomain)
	})
	app.Get("/auth-only", tenant.RequireAuth(), func(c *fiber.Ctx) error {
		return c.SendString("auth-ok")
	})
	return app, db
}

func get(t *testing.T, app *fiber.App, host string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/tenant-only", nil)
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request to %s: %v", host, err)
	}
	return resp
}

func TestResolveHostTable(t *testing.T) {
	app, db := newTestApp(t, false)

	orgID := mustInsertTestOrg(t, db, "javan", nil)
	_ = orgID
	deletedAt := time.Now()
	mustInsertTestOrg(t, db, "gone", &deletedAt)
	if _, err := db.Exec(`INSERT INTO retired_slugs (slug, org_id) VALUES ('retired', $1)`, orgID); err != nil {
		t.Fatalf("insert retired slug: %v", err)
	}

	cases := []struct {
		name       string
		host       string
		wantStatus int
		wantBody   string
	}{
		{"exact match", "javan." + testBaseDomain, 200, "tenant:javan"},
		{"port stripped", "javan." + testBaseDomain + ":8443", 200, "tenant:javan"},
		{"case insensitive", "JAVAN." + testBaseDomain, 200, "tenant:javan"},
		{"unknown slug", "nonexistent." + testBaseDomain, 404, ""},
		{"reserved slug", "www." + testBaseDomain, 404, ""},
		{"deleted org", "gone." + testBaseDomain, 404, ""},
		{"retired slug", "retired." + testBaseDomain, 404, ""},
		{"no base-domain suffix", "1.2.3.4", 404, ""},
		{"unrelated domain", "evil.example.com", 404, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := get(t, app, c.host, nil)
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("host %q: status = %d, want %d", c.host, resp.StatusCode, c.wantStatus)
			}
		})
	}
}

func TestCrossHostRejection(t *testing.T) {
	app, db := newTestApp(t, false)
	mustInsertTestOrg(t, db, "javan", nil)

	// Tenant-only route on the auth host -> 404.
	resp := get(t, app, "auth."+testBaseDomain, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tenant route on auth host: status = %d, want 404", resp.StatusCode)
	}

	// Auth-only route on a tenant host -> 404.
	req := httptest.NewRequest(http.MethodGet, "http://javan."+testBaseDomain+"/auth-only", nil)
	req.Host = "javan." + testBaseDomain
	authResp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if authResp.StatusCode != http.StatusNotFound {
		t.Fatalf("auth route on tenant host: status = %d, want 404", authResp.StatusCode)
	}

	// Auth-only route on the auth host -> 200.
	req2 := httptest.NewRequest(http.MethodGet, "http://auth."+testBaseDomain+"/auth-only", nil)
	req2.Host = "auth." + testBaseDomain
	authOK, err := app.Test(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if authOK.StatusCode != http.StatusOK {
		t.Fatalf("auth route on auth host: status = %d, want 200", authOK.StatusCode)
	}
}

func TestDevOrgHeaderGatedOnDevMode(t *testing.T) {
	// Dev mode off: header must be ignored, so an otherwise-unresolvable
	// host still 404s even with a valid org name in the header.
	appOff, db := newTestApp(t, false)
	mustInsertTestOrg(t, db, "javan", nil)
	resp := get(t, appOff, "localhost", map[string]string{tenant.DevOrgHeader: "javan"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("LOTUS_DEV unset: status = %d, want 404 (header must be ignored)", resp.StatusCode)
	}

	// Dev mode on: the same header resolves the org directly.
	appOn, db2 := newTestApp(t, true)
	mustInsertTestOrg(t, db2, "javan", nil)
	resp2 := get(t, appOn, "localhost", map[string]string{tenant.DevOrgHeader: "javan"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("LOTUS_DEV=1: status = %d, want 200", resp2.StatusCode)
	}
}

func mustInsertTestOrg(t *testing.T, db *sqlx.DB, subdomain string, deletedAt *time.Time) string {
	t.Helper()
	var id string
	err := db.Get(&id,
		`INSERT INTO organizations (subdomain, name, deleted_at) VALUES ($1, $1, $2) RETURNING id`,
		subdomain, deletedAt)
	if err != nil {
		t.Fatalf("insert org %s: %v", subdomain, err)
	}
	return id
}

// newSingleModeApp builds the same two guarded routes as newTestApp, but
// with a resolver in single mode — where both guards must pass on one
// origin, whatever Host the request carries.
func newSingleModeApp(t *testing.T) (*fiber.App, *sqlx.DB) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008", Dev: true}
	resolver := tenant.NewResolver(cfg, db)

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/tenant-only", tenant.RequireTenant(), func(c *fiber.Ctx) error {
		org, _ := tenant.OrgFromContext(c)
		return c.SendString("tenant:" + org.Subdomain)
	})
	app.Get("/auth-only", tenant.RequireAuth(), func(c *fiber.Ctx) error {
		return c.SendString("auth-ok")
	})
	return app, db
}

// TestSingleMode_OneOriginSatisfiesBothGuards is the core acceptance
// criterion of single-tenant mode: RequireAuth and RequireTenant are
// mutually exclusive in saas mode, and must both pass here, on the same
// origin, for any Host at all — a self-hoster may reach the app by
// hostname, by bare IP, or through a proxy that rewrites Host entirely.
func TestSingleMode_OneOriginSatisfiesBothGuards(t *testing.T) {
	app, db := newSingleModeApp(t)
	mustInsertTestOrg(t, db, "javan", nil)

	for _, host := range []string{"localhost:8008", "192.0.2.10:8008", "lotus.acme.internal"} {
		for _, path := range []string{"/tenant-only", "/auth-only"} {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
			req.Host = host
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("%s %s: %v", host, path, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s: status = %d, want 200", host, path, resp.StatusCode)
			}
		}
	}
}

// TestSingleMode_IgnoresDevOrgHeader: X-Lotus-Org selects between tenants,
// and in single mode there is nothing to select between. Honoring it would
// be a way to ask for an organization that cannot exist.
func TestSingleMode_IgnoresDevOrgHeader(t *testing.T) {
	app, db := newSingleModeApp(t)
	mustInsertTestOrg(t, db, "javan", nil)

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8008/tenant-only", nil)
	req.Host = "localhost:8008"
	req.Header.Set(tenant.DevOrgHeader, "someoneelse")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the header must be ignored, not honored or rejected)", resp.StatusCode)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if got := string(body[:n]); got != "tenant:javan" {
		t.Fatalf("body = %q, want tenant:javan", got)
	}
}

// TestSingleMode_NoOrgYetIs404 covers the window between `lotus migrate`
// and `lotus init`: the database is up but no organization exists. Every
// request must 404 rather than panic on a nil org.
func TestSingleMode_NoOrgYetIs404(t *testing.T) {
	app, _ := newSingleModeApp(t)

	for _, path := range []string{"/tenant-only", "/auth-only"} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8008"+path, nil)
		req.Host = "localhost:8008"
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404 before lotus init has run", path, resp.StatusCode)
		}
	}
}
