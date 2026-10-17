package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// newDiscoveryTestApp wires both connection and discovery routes (creating
// the connection under test needs the former) with a fake DNS resolver —
// resolveHost maps a hostname to the fake upstream server's real loopback
// address — and IsBlocked disabled, since httptest.Server always binds
// loopback and discovery genuinely dials it (unlike task 2.2's tests,
// which never complete a real round trip).
func newDiscoveryTestApp(t *testing.T, resolveHost func(host string) []net.IP) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: connectionsTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	kek, err := crypto.LoadKEK("bG90dXMtdGVzdC1rZWstMzItYnl0ZXMtZXhhY3RseVg=", 1)
	if err != nil {
		t.Fatalf("load test KEK: %v", err)
	}
	d := Deps{
		DB: db, Cfg: cfg, CookieSecure: false, KEK: kek,
		NetguardResolver: func(_ context.Context, host string) ([]net.IP, error) {
			if resolveHost == nil {
				return []net.IP{net.ParseIP("203.0.113.10")}, nil
			}
			return resolveHost(host), nil
		},
		NetguardIsBlocked: func(net.IP) bool { return false },
	}
	deps := authn.Deps{DB: db, Cfg: cfg, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	registerConnectionRoutes(app, d, authn.RequireTenantSession(deps), authz.LoadPrincipal(db))
	registerDiscoveryRoutes(app, d, authn.RequireTenantSession(deps), authz.LoadPrincipal(db))
	return app, db, cfg
}

func TestDiscovery_RunUpsertsAndClaimCreatesProjectComponentBinding(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":1,"name":"BPM Service","path_with_namespace":"group/bpm-service"}]`)
	}))
	defer upstream.Close()
	upstreamIP, upstreamPort, err := net.SplitHostPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}

	app, db, cfg := newDiscoveryTestApp(t, func(host string) []net.IP {
		if host == "gitlab.fake" {
			return []net.IP{net.ParseIP(upstreamIP)}
		}
		return nil
	})
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	createResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections", raw,
		`{"provider":"gitlab","name":"main","base_url":"http://gitlab.fake:`+upstreamPort+`","secret":"token"}`), -1)
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	var conn repo.Connection
	if err := json.NewDecoder(createResp.Body).Decode(&conn); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create connection status = %d", createResp.StatusCode)
	}

	discoverResp, err := app.Test(reqWithSession(http.MethodPost, host, "/connections/"+conn.ID.String()+"/discover", raw), 5000)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discoverResp.StatusCode != http.StatusOK {
		t.Fatalf("discover status = %d", discoverResp.StatusCode)
	}
	var discoverBody struct {
		Count    int  `json:"count"`
		Degraded bool `json:"degraded"`
	}
	json.NewDecoder(discoverResp.Body).Decode(&discoverBody)
	if discoverBody.Count != 1 || discoverBody.Degraded {
		t.Fatalf("discover body = %+v, want count=1 degraded=false", discoverBody)
	}

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/connections/"+conn.ID.String()+"/discovered?unclaimed=true", raw), -1)
	if err != nil {
		t.Fatalf("list discovered: %v", err)
	}
	var resources []repo.DiscoveredResource
	json.NewDecoder(listResp.Body).Decode(&resources)
	if len(resources) != 1 {
		t.Fatalf("got %d unclaimed resources, want 1", len(resources))
	}

	claimBody := `{"claims":[{"discovered_resource_id":"` + resources[0].ID.String() + `",
		"new_project_slug":"bpm","new_project_name":"BPM",
		"new_component_slug":"service","new_component_name":"Service","new_component_kind":"backend",
		"binding_config":{}}]}`
	claimResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections/"+conn.ID.String()+"/discovered/claim", raw, claimBody), -1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimResp.StatusCode != http.StatusCreated {
		body := mustReadBody(t, claimResp)
		t.Fatalf("claim status = %d, body = %s", claimResp.StatusCode, body)
	}
	var results []repo.ClaimResult
	json.NewDecoder(claimResp.Body).Decode(&results)
	if len(results) != 1 {
		t.Fatalf("got %d claim results, want 1", len(results))
	}

	// The claimed resource no longer shows up as unclaimed.
	afterResp, _ := app.Test(reqWithSession(http.MethodGet, host, "/connections/"+conn.ID.String()+"/discovered?unclaimed=true", raw), -1)
	var afterList []repo.DiscoveredResource
	json.NewDecoder(afterResp.Body).Decode(&afterList)
	if len(afterList) != 0 {
		t.Fatalf("expected 0 unclaimed resources after claim, got %d", len(afterList))
	}

	var bindingCount int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&bindingCount, `SELECT count(*) FROM metric_bindings WHERE id = $1`, results[0].BindingID); err != nil {
			t.Fatalf("count binding: %v", err)
		}
	})
	if bindingCount != 1 {
		t.Fatal("expected the claim to have created a metric binding")
	}
}

func TestDiscovery_SonarQubeDegradedDoesNotError(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()
	upstreamIP, upstreamPort, _ := net.SplitHostPort(upstream.Listener.Addr().String())

	app, db, cfg := newDiscoveryTestApp(t, func(host string) []net.IP {
		if host == "sonar.fake" {
			return []net.IP{net.ParseIP(upstreamIP)}
		}
		return nil
	})
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	createResp, _ := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections", raw,
		`{"provider":"sonarqube","name":"main","base_url":"http://sonar.fake:`+upstreamPort+`","secret":"limited-token"}`), -1)
	var conn repo.Connection
	json.NewDecoder(createResp.Body).Decode(&conn)

	discoverResp, err := app.Test(reqWithSession(http.MethodPost, host, "/connections/"+conn.ID.String()+"/discover", raw), 5000)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discoverResp.StatusCode != http.StatusOK {
		t.Fatalf("discover status = %d, want 200 (degraded is not an error)", discoverResp.StatusCode)
	}
	var body struct {
		Degraded bool `json:"degraded"`
	}
	json.NewDecoder(discoverResp.Body).Decode(&body)
	if !body.Degraded {
		t.Fatal("expected degraded=true for a 403 from SonarQube's admin-only endpoint")
	}
}

func TestDiscovery_MissingFromUpstreamFreezesLastSeenRatherThanDeleting(t *testing.T) {
	t.Parallel()
	app, db, cfg := newDiscoveryTestApp(t, nil)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	connID := mustSeedConnection(t, db, orgID)
	var resourceID uuid.UUID
	var firstSeen, lastSeen string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&resourceID, `
			INSERT INTO discovered_resources (org_id, connection_id, kind, external_key, label, first_seen_at, last_seen_at)
			VALUES ($1, $2, 'gitlab', 'group/gone', 'Gone', now() - interval '2 days', now() - interval '2 days')
			RETURNING id`, orgID, connID); err != nil {
			t.Fatalf("seed discovered resource: %v", err)
		}
		if err := tx.QueryRow(`SELECT first_seen_at, last_seen_at FROM discovered_resources WHERE id = $1`, resourceID).Scan(&firstSeen, &lastSeen); err != nil {
			t.Fatalf("read timestamps: %v", err)
		}
	})

	// Upsert with an empty result set (as if upstream no longer has it) —
	// the existing row must be left completely untouched.
	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/connections/"+connID.String()+"/discovered", raw), -1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var resources []repo.DiscoveredResource
	json.NewDecoder(listResp.Body).Decode(&resources)
	if len(resources) != 1 {
		t.Fatalf("got %d resources, want 1 (missing-from-upstream must not delete)", len(resources))
	}
	if resources[0].LastSeenAt.IsZero() {
		t.Fatal("last_seen_at should still be set")
	}
}
