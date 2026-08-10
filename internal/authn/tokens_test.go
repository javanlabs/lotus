package authn_test

import (
	"context"
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
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// bearerReq builds a GET/POST request carrying an Authorization: Bearer
// header for the raw token — task 1.8's auth path.
func bearerReq(method, url, rawToken string) *http.Request {
	req := httptest.NewRequest(method, url, nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	return req
}

func TestParseAPIToken_RoundTrip(t *testing.T) {
	for _, kind := range []string{"pat", "service"} {
		raw, hash, err := authn.GenerateAPIToken(kind)
		if err != nil {
			t.Fatalf("generate %s: %v", kind, err)
		}
		gotKind, gotHash, err := authn.ParseAPIToken(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", kind, err)
		}
		if gotKind != kind {
			t.Errorf("kind = %q, want %q", gotKind, kind)
		}
		if gotHash != hash {
			t.Errorf("hash mismatch for %s", kind)
		}
	}
}

func TestParseAPIToken_BadChecksumRejectedWithoutDB(t *testing.T) {
	raw, _, err := authn.GenerateAPIToken("pat")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Flip the last character, corrupting the checksum.
	corrupted := raw[:len(raw)-1] + flipChar(raw[len(raw)-1])
	if _, _, err := authn.ParseAPIToken(corrupted); err != authn.ErrBadTokenChecksum {
		t.Fatalf("err = %v, want ErrBadTokenChecksum", err)
	}
}

func TestParseAPIToken_UnknownPrefixRejected(t *testing.T) {
	if _, _, err := authn.ParseAPIToken("not_a_lotus_token_at_all"); err != authn.ErrBadTokenChecksum {
		t.Fatalf("err = %v, want ErrBadTokenChecksum", err)
	}
}

func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func newTokenTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config, authn.Deps) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/sync", tenant.RequireTenant(),
		authn.RequireTokenOrSession(deps), authn.RequireTenantSession(deps), authz.LoadPrincipal(db),
		authz.Require("sync.trigger"),
		func(c *fiber.Ctx) error { return c.SendString("synced") })
	// Illustrative of task 3.4's future ingest endpoint: enforces a
	// service token's component pin itself, since only it knows which
	// component the request is about.
	app.Post("/components/:id/metrics", tenant.RequireTenant(),
		authn.RequireTokenOrSession(deps), authn.RequireTenantSession(deps), authz.LoadPrincipal(db),
		authz.Require("metrics:write"),
		func(c *fiber.Ctx) error {
			p, _ := authz.CurrentPrincipal(c)
			if p.PinnedComponentID != uuid.Nil && p.PinnedComponentID.String() != c.Params("id") {
				return fiber.ErrNotFound
			}
			return c.SendString("written")
		})
	return app, db, cfg, deps
}

func TestPAT_DemotedOwnerLosesWriteAccessNextRequest(t *testing.T) {
	app, db, cfg, deps := newTokenTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme") // seeded as owner

	raw, err := authn.IssuePersonalAccessToken(context.Background(), deps, authn.NewPersonalAccessTokenParams{
		UserID: userID, Name: "ci", Scopes: []string{"sync.trigger"},
	})
	if err != nil {
		t.Fatalf("issue PAT: %v", err)
	}

	req1 := bearerReq(http.MethodGet, "http://acme."+cfg.BaseDomain+"/sync", raw)
	req1.Host = "acme." + cfg.BaseDomain
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("status 1 = %d, want 200", resp1.StatusCode)
	}

	addSecondOwner(t, db, orgID) // so demoting userID doesn't trip the >=1-Owner trigger
	viewerRoleID := mustRoleIDForTest(t, db, "viewer")
	if err := global.UpdateMembershipRole(context.Background(), db, orgID, userID, viewerRoleID); err != nil {
		t.Fatalf("demote: %v", err)
	}

	req2 := bearerReq(http.MethodGet, "http://acme."+cfg.BaseDomain+"/sync", raw)
	req2.Host = "acme." + cfg.BaseDomain
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("status 2 = %d, want 403 (demoted owner's token must lose access without revocation)", resp2.StatusCode)
	}
}

func TestServiceToken_CrossOrgHostRejected(t *testing.T) {
	app, db, cfg, deps := newTokenTestApp(t)
	orgA, ownerID := seedActiveMember(t, db, "acme")
	orgB, _ := seedActiveMember(t, db, "beta")
	_ = orgB

	raw, err := authn.IssueServiceToken(context.Background(), deps, authn.NewServiceTokenParams{
		OrgID: orgA, ActorID: ownerID, ActorRole: "owner", Name: "ci", Scopes: []string{"metrics:write"},
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	req := bearerReq(http.MethodPost, "http://beta."+cfg.BaseDomain+"/components/"+uuid.New().String()+"/metrics", raw)
	req.Host = "beta." + cfg.BaseDomain
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (token minted for a different org)", resp.StatusCode)
	}
}

func TestServiceToken_ComponentPinEnforced(t *testing.T) {
	app, db, cfg, deps := newTokenTestApp(t)
	orgID, ownerID := seedActiveMember(t, db, "acme")

	var projectID uuid.UUID
	if err := db.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var componentA, componentB uuid.UUID
	if err := db.Get(&componentA, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'a', 'A', 'service') RETURNING id`, orgID, projectID); err != nil {
		t.Fatalf("insert component A: %v", err)
	}
	if err := db.Get(&componentB, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'b', 'B', 'service') RETURNING id`, orgID, projectID); err != nil {
		t.Fatalf("insert component B: %v", err)
	}

	raw, err := authn.IssueServiceToken(context.Background(), deps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: ownerID, ActorRole: "owner", Name: "ci",
		Scopes: []string{"metrics:write"}, ComponentID: &componentA,
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	// Writing to the pinned component succeeds.
	reqA := bearerReq(http.MethodPost, "http://acme."+cfg.BaseDomain+"/components/"+componentA.String()+"/metrics", raw)
	reqA.Host = "acme." + cfg.BaseDomain
	respA, err := app.Test(reqA)
	if err != nil {
		t.Fatalf("request A: %v", err)
	}
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("pinned component status = %d, want 200", respA.StatusCode)
	}

	// Writing to a different component is rejected.
	reqB := bearerReq(http.MethodPost, "http://acme."+cfg.BaseDomain+"/components/"+componentB.String()+"/metrics", raw)
	reqB.Host = "acme." + cfg.BaseDomain
	respB, err := app.Test(reqB)
	if err != nil {
		t.Fatalf("request B: %v", err)
	}
	if respB.StatusCode != http.StatusNotFound {
		t.Fatalf("unpinned component status = %d, want 404", respB.StatusCode)
	}
}

func TestServiceToken_SurvivesCreatorDeletion(t *testing.T) {
	_, db, _, deps := newTokenTestApp(t)
	orgID, ownerID := seedActiveMember(t, db, "acme")

	raw, err := authn.IssueServiceToken(context.Background(), deps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: ownerID, ActorRole: "owner", Name: "ci", Scopes: []string{"metrics:write"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	_, hash, err := authn.ParseAPIToken(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	addSecondOwner(t, db, orgID) // so deleting ownerID doesn't trip the >=1-Owner trigger
	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, ownerID); err != nil {
		t.Fatalf("delete creator: %v", err)
	}

	tok, err := global.APITokenByHash(context.Background(), db, hash)
	if err != nil {
		t.Fatalf("token should survive its creator's deletion: %v", err)
	}
	if tok.CreatedBy != nil {
		t.Error("expected created_by to be cleared, not the token deleted")
	}
}

func TestRequireTokenOrSession_BothCookieAndBearerRejected(t *testing.T) {
	app, db, cfg, deps := newTokenTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme")

	raw, err := authn.IssuePersonalAccessToken(context.Background(), deps, authn.NewPersonalAccessTokenParams{
		UserID: userID, Name: "ci", Scopes: []string{"sync.trigger"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	sessionRaw := seedTenantSession(t, db, orgID, userID, 0)

	req := bearerReq(http.MethodGet, "http://acme."+cfg.BaseDomain+"/sync", raw)
	req.Host = "acme." + cfg.BaseDomain
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: sessionRaw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cookie + bearer together must be rejected)", resp.StatusCode)
	}
}

func TestQueryParamTokenNeverAccepted(t *testing.T) {
	app, db, cfg, _ := newTokenTestApp(t)
	_, userID := seedActiveMember(t, db, "acme")
	// A raw PAT-shaped value in the query string must not authenticate —
	// task 1.8 explicitly removes the old ?token= path, it never existed
	// in this codebase, and this test is here so it's never reintroduced
	// by accident.
	req := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/sync?token=lotus_pat_notarealtoken")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a query-string token must never authenticate a request")
	}
	_ = userID
}
