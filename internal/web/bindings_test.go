package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func newBindingsTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: connectionsTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	kek, err := crypto.LoadKEK("bG90dXMtdGVzdC1rZWstMzItYnl0ZXMtZXhhY3RseVg=", 1)
	if err != nil {
		t.Fatalf("load test KEK: %v", err)
	}
	d := Deps{DB: db, Cfg: cfg, CookieSecure: false, KEK: kek, NetguardResolver: fakePublicDNS}
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	registerBindingRoutes(app, d, authn.RequireTenantSession(deps), authz.LoadPrincipal(db))
	return app, db, cfg
}

func TestBindings_TwoGitLabBindingsOnSameComponentAllowed(t *testing.T) {
	app, db, cfg := newBindingsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgID, "web")
	componentID := mustSeedComponent(t, db, orgID, projectID, "app")
	connID := mustSeedConnection(t, db, orgID)

	body1 := `{"connection_id":"` + connID.String() + `","kind":"gitlab","external_key":"group/frontend","config":{}}`
	resp1, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw, body1))
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("create 1 status = %d, want 201", resp1.StatusCode)
	}

	body2 := `{"connection_id":"` + connID.String() + `","kind":"gitlab","external_key":"group/backend","config":{}}`
	resp2, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw, body2))
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create 2 (second GitLab binding, different external_key, same component) status = %d, want 201", resp2.StatusCode)
	}

	// Same kind + external_key on the same component is rejected.
	resp3, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw, body1))
	if err != nil {
		t.Fatalf("create 3: %v", err)
	}
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("create 3 (duplicate kind+external_key) status = %d, want 409", resp3.StatusCode)
	}
}

func TestBindings_ConnectionFromAnotherOrgRejected(t *testing.T) {
	app, db, cfg := newBindingsTestApp(t)
	orgA, userA := seedMember(t, db, "acme", "owner")
	orgB, _ := seedMember(t, db, "beta", "owner")
	raw := seedSession(t, db, orgA, userA)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgA, "web")
	componentID := mustSeedComponent(t, db, orgA, projectID, "app")
	connInOrgB := mustSeedConnection(t, db, orgB)

	body := `{"connection_id":"` + connInOrgB.String() + `","kind":"gitlab","external_key":"group/project","config":{}}`
	resp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw, body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("expected a binding pointing at another organization's connection to be rejected")
	}
}

func TestBindings_SentrySharedModeWithoutTagValueRejectedInGoAndDB(t *testing.T) {
	app, db, cfg := newBindingsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgID, "web")
	componentID := mustSeedComponent(t, db, orgID, projectID, "app")
	connID := mustSeedConnection(t, db, orgID)

	// Rejected in Go (never reaches the database).
	body := `{"connection_id":"` + connID.String() + `","kind":"sentry","external_key":"app","config":{"mode":"shared"}}`
	resp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw, body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (rejected by Descriptor.ValidateConfig)", resp.StatusCode)
	}

	// Rejected in the database too (migration 00012's CHECK), bypassing Go
	// entirely via a direct insert — proves the CHECK is a real safety net,
	// not just documentation. Not using withOrgTx: this insert is expected
	// to fail and abort the transaction, so committing afterward isn't the
	// right assertion — just that BeginTx-set_config-Exec surfaces the
	// constraint violation.
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	_, insertErr := tx.Exec(`
		INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
		VALUES ($1, $2, $3, 'sentry', 'app', '{"mode":"shared"}'::jsonb)`,
		orgID, componentID, connID)
	if insertErr == nil {
		t.Fatal("expected the database CHECK constraint to reject mode=shared without service_tag_value")
	}
}

func TestBindings_DisableStopsSchedulingWithoutDeletingHistory(t *testing.T) {
	app, db, cfg := newBindingsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgID, "web")
	componentID := mustSeedComponent(t, db, orgID, projectID, "app")
	connID := mustSeedConnection(t, db, orgID)

	createResp, _ := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw,
		`{"connection_id":"`+connID.String()+`","kind":"gitlab","external_key":"group/project","config":{}}`))
	var created repo.Binding
	json.NewDecoder(createResp.Body).Decode(&created)

	disableResp, err := app.Test(reqWithSession(http.MethodPost, host, "/bindings/"+created.ID.String()+"/disable", raw))
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disableResp.StatusCode != http.StatusNoContent {
		t.Fatalf("disable status = %d, want 204", disableResp.StatusCode)
	}

	getResp, err := app.Test(reqWithSession(http.MethodGet, host, "/bindings/"+created.ID.String(), raw))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var got repo.Binding
	json.NewDecoder(getResp.Body).Decode(&got)
	if got.Enabled {
		t.Fatal("expected the binding to be disabled")
	}
	if got.DeletedAt != nil {
		t.Fatal("disabling must not soft-delete the binding")
	}
}

func TestBindings_SoftDeletePreservesRowButHidesFromList(t *testing.T) {
	app, db, cfg := newBindingsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	projectID := mustSeedProject(t, db, orgID, "web")
	componentID := mustSeedComponent(t, db, orgID, projectID, "app")
	connID := mustSeedConnection(t, db, orgID)

	createResp, _ := app.Test(jsonReqWithSession(http.MethodPost, host, "/components/"+componentID.String()+"/bindings", raw,
		`{"connection_id":"`+connID.String()+`","kind":"gitlab","external_key":"group/project","config":{}}`))
	var created repo.Binding
	json.NewDecoder(createResp.Body).Decode(&created)

	delResp, err := app.Test(reqWithSession(http.MethodDelete, host, "/bindings/"+created.ID.String(), raw))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", delResp.StatusCode)
	}

	listResp, _ := app.Test(reqWithSession(http.MethodGet, host, "/components/"+componentID.String()+"/bindings", raw))
	var list []repo.Binding
	json.NewDecoder(listResp.Body).Decode(&list)
	if len(list) != 0 {
		t.Fatalf("default list should hide soft-deleted bindings, got %d", len(list))
	}

	var stillExists bool
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&stillExists, `SELECT EXISTS(SELECT 1 FROM metric_bindings WHERE id = $1)`, created.ID); err != nil {
			t.Fatalf("check row: %v", err)
		}
	})
	if !stillExists {
		t.Fatal("soft delete must preserve the row for history")
	}
}
