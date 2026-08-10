package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	_ "github.com/uyab/lotus/internal/collector/trivy" // registers the trivy Collector/Ingestor/Persister
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/tenant"
)

const ingestTestDomain = "lotus.test"

func newIngestTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config, authn.Deps) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: ingestTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	d := Deps{DB: db, Cfg: cfg, CookieSecure: false}
	authDeps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	registerIngestRoutes(app, d, authDeps)
	return app, db, cfg, authDeps
}

func withOrgTxIngest(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// seedIngestFixture creates an org (with an Owner user to satisfy
// api_tokens.created_by's FK) and two components, each carrying one Trivy
// binding — enough to exercise the service token's component pin against
// both the matching and a mismatched binding.
func seedIngestFixture(t *testing.T, db *sqlx.DB, subdomain string) (orgID, userID, compA, bindA, compB, bindB uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, subdomain+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var ownerRoleID uuid.UUID
	if err := db.Get(&ownerRoleID, `SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		t.Fatalf("lookup owner role: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, ownerRoleID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	withOrgTxIngest(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&compA, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'a', 'A', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component A: %v", err)
		}
		if err := tx.Get(&compB, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'b', 'B', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component B: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'harbor', 'https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindA, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, 'trivy', 'proj/a') RETURNING id`, orgID, compA, connID); err != nil {
			t.Fatalf("insert binding A: %v", err)
		}
		if err := tx.Get(&bindB, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, 'trivy', 'proj/b') RETURNING id`, orgID, compB, connID); err != nil {
			t.Fatalf("insert binding B: %v", err)
		}
	})
	return orgID, userID, compA, bindA, compB, bindB
}

const ingestFSReportFixture = `{"Results":[{"Target":"go.mod","Type":"gomod","Vulnerabilities":[
	{"VulnerabilityID":"CVE-2026-1","PkgName":"golang.org/x/net","InstalledVersion":"0.1.0","Severity":"CRITICAL"}
]}]}`

func TestIngestTrivy_ServiceTokenPinnedToComponentSucceeds(t *testing.T) {
	app, db, cfg, authDeps := newIngestTestApp(t)
	orgID, userID, compA, bindA, _, _ := seedIngestFixture(t, db, "acme")

	raw, err := authn.IssueServiceToken(context.Background(), authDeps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: userID, ActorRole: "admin", Name: "ci",
		Scopes: []string{"metrics.ingest"}, ComponentID: &compA,
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	url := "http://acme." + cfg.BaseDomain + "/ingest/trivy/" + bindA.String()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(ingestFSReportFixture))
	req.Host = "acme." + cfg.BaseDomain
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
}

func TestIngestTrivy_ServiceTokenPinnedToOtherComponentIs404(t *testing.T) {
	app, db, cfg, authDeps := newIngestTestApp(t)
	orgID, userID, compA, _, _, bindB := seedIngestFixture(t, db, "acme")

	raw, err := authn.IssueServiceToken(context.Background(), authDeps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: userID, ActorRole: "admin", Name: "ci",
		Scopes: []string{"metrics.ingest"}, ComponentID: &compA,
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	// bindB belongs to compB, not the token's pinned compA.
	url := "http://acme." + cfg.BaseDomain + "/ingest/trivy/" + bindB.String()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(ingestFSReportFixture))
	req.Host = "acme." + cfg.BaseDomain
	req.Header.Set("Authorization", "Bearer "+raw)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (pinned component mismatch)", resp.StatusCode)
	}
}

func TestIngestTrivy_UnpinnedServiceTokenReachesAnyBindingInOrg(t *testing.T) {
	app, db, cfg, authDeps := newIngestTestApp(t)
	orgID, userID, _, _, _, bindB := seedIngestFixture(t, db, "acme")

	raw, err := authn.IssueServiceToken(context.Background(), authDeps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: userID, ActorRole: "admin", Name: "ci-org-wide",
		Scopes: []string{"metrics.ingest"},
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	url := "http://acme." + cfg.BaseDomain + "/ingest/trivy/" + bindB.String()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(ingestFSReportFixture))
	req.Host = "acme." + cfg.BaseDomain
	req.Header.Set("Authorization", "Bearer "+raw)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (no pin means any binding in this org)", resp.StatusCode)
	}
}

func TestIngestTrivy_TokenWithoutIngestScopeIsForbidden(t *testing.T) {
	app, db, cfg, authDeps := newIngestTestApp(t)
	orgID, userID, compA, bindA, _, _ := seedIngestFixture(t, db, "acme")

	raw, err := authn.IssueServiceToken(context.Background(), authDeps, authn.NewServiceTokenParams{
		OrgID: orgID, ActorID: userID, ActorRole: "admin", Name: "ci-read-only",
		Scopes: []string{"dashboard.read"}, ComponentID: &compA,
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	url := "http://acme." + cfg.BaseDomain + "/ingest/trivy/" + bindA.String()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(ingestFSReportFixture))
	req.Host = "acme." + cfg.BaseDomain
	req.Header.Set("Authorization", "Bearer "+raw)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (token lacks metrics.ingest scope)", resp.StatusCode)
	}
}

func TestIngestTrivy_CrossOrgServiceTokenIs404(t *testing.T) {
	app, db, cfg, authDeps := newIngestTestApp(t)
	orgA, userA, _, _, _, _ := seedIngestFixture(t, db, "acme")
	_, _, _, bindBeta, _, _ := seedIngestFixture(t, db, "beta")

	raw, err := authn.IssueServiceToken(context.Background(), authDeps, authn.NewServiceTokenParams{
		OrgID: orgA, ActorID: userA, ActorRole: "admin", Name: "ci",
		Scopes: []string{"metrics.ingest"},
	})
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}

	// A token minted for org "acme" presented against org "beta"'s binding
	// on beta's own host.
	url := "http://beta." + cfg.BaseDomain + "/ingest/trivy/" + bindBeta.String()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(ingestFSReportFixture))
	req.Host = "beta." + cfg.BaseDomain
	req.Header.Set("Authorization", "Bearer "+raw)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (service token minted for a different org)", resp.StatusCode)
	}
}
