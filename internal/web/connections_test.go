package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
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

const connectionsTestDomain = "lotus.test"

// fakePublicDNS stands in for real DNS in these tests, so connection-
// creation tests don't depend on outbound network access reaching a real
// resolver from within the test sandbox. Mirrors real DNS behavior for the
// two host shapes the tests actually use: a literal IP resolves to itself
// (169.254.169.254 in the SSRF test must still resolve to the blocked
// address it literally is), "localhost" resolves to 127.0.0.1 (also still
// correctly blocked), and every other hostname maps to a public,
// non-blocked address.
func fakePublicDNS(_ context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if host == "localhost" {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	return []net.IP{net.ParseIP("203.0.113.10")}, nil
}

func newConnectionsTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
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
	registerConnectionRoutes(app, d, authn.RequireTenantSession(deps), authz.LoadPrincipal(db))
	return app, db, cfg
}

func TestConnections_CreateThenGet_SecretNeverInResponse(t *testing.T) {
	app, db, cfg := newConnectionsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	const secret = "glpat-supersecrettoken12345"
	body := `{"provider":"gitlab","name":"main","base_url":"https://gitlab.example.com","secret":"` + secret + `"}`
	resp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections", raw, body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rawBody := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body=%s", resp.StatusCode, rawBody)
	}
	if strings.Contains(rawBody, secret) {
		t.Fatalf("secret leaked in create response: %s", rawBody)
	}
	var created repo.Connection
	if err := json.Unmarshal([]byte(rawBody), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.SecretLast4 == nil || *created.SecretLast4 != "2345" {
		t.Fatalf("secret_last4 = %v, want 2345", created.SecretLast4)
	}

	getResp, err := app.Test(reqWithSession(http.MethodGet, host, "/connections/"+created.ID.String(), raw))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	getBody := mustReadBody(t, getResp)
	if strings.Contains(getBody, secret) {
		t.Fatalf("secret leaked in get response: %s", getBody)
	}

	listResp, err := app.Test(reqWithSession(http.MethodGet, host, "/connections", raw))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listBody := mustReadBody(t, listResp)
	if strings.Contains(listBody, secret) {
		t.Fatalf("secret leaked in list response: %s", listBody)
	}
}

func TestConnections_CiphertextNotDecryptableAcrossOrgs(t *testing.T) {
	app, db, cfg := newConnectionsTestApp(t)
	orgA, userA := seedMember(t, db, "acme", "owner")
	orgB, userB := seedMember(t, db, "beta", "owner")
	rawA := seedSession(t, db, orgA, userA)
	rawB := seedSession(t, db, orgB, userB)

	body := `{"provider":"gitlab","name":"main","base_url":"https://gitlab.example.com","secret":"glpat-secret"}`
	resp, err := app.Test(jsonReqWithSession(http.MethodPost, "acme."+cfg.BaseDomain, "/connections", rawA, body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created repo.Connection
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Copy the ciphertext row into org B's own connection, as if a
	// malicious DB write tried to relocate it — decrypting it there must
	// fail (AAD is the *owning* org's id, task 2.2's core guarantee). Read
	// under org A's scope and insert under org B's scope as two separate
	// transactions: RLS means a single org-B-scoped transaction can't even
	// see org A's source row to select it from in the first place.
	var provider, name, baseURL string
	var ciphertext, nonce []byte
	withOrgTx(t, db, orgA, func(tx *sqlx.Tx) {
		if err := tx.QueryRow(`
			SELECT provider, name, base_url, secret_ciphertext, secret_nonce
			FROM integration_connections WHERE id = $1`, created.ID).
			Scan(&provider, &name, &baseURL, &ciphertext, &nonce); err != nil {
			t.Fatalf("read org A's connection: %v", err)
		}
	})
	var newID uuid.UUID
	withOrgTx(t, db, orgB, func(tx *sqlx.Tx) {
		if err := tx.Get(&newID, `
			INSERT INTO integration_connections (org_id, provider, name, base_url, secret_ciphertext, secret_nonce)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`, orgB, provider, name, baseURL, ciphertext, nonce); err != nil {
			t.Fatalf("copy ciphertext into org B: %v", err)
		}
	})

	// Cross-org: acme's user asking for org B's row is 404 anyway (RLS) —
	// exercise the decrypt path as org B itself, which owns the row (so
	// RLS lets it through) but not the DEK the ciphertext was actually
	// sealed under, to prove decryption specifically fails there. The
	// handler's decrypt step fails before it ever reaches a live upstream
	// call or SetConnectionTestResult, surfacing as a non-2xx response
	// rather than a 200 body with status="error".
	testResp, err := app.Test(jsonReqWithSession(http.MethodPost, "beta."+cfg.BaseDomain, "/connections/"+newID.String()+"/test", rawB, `{}`))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if testResp.StatusCode == http.StatusOK {
		t.Fatalf("status = %d, want a failure (ciphertext copied across orgs must fail to decrypt)", testResp.StatusCode)
	}
}

func TestConnections_BaseURLChangeRequiresSecretAndRecordsAudit(t *testing.T) {
	app, db, cfg := newConnectionsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	body := `{"provider":"gitlab","name":"main","base_url":"https://gitlab.example.com","secret":"glpat-secret"}`
	resp, _ := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections", raw, body))
	var created repo.Connection
	json.NewDecoder(resp.Body).Decode(&created)

	// Missing secret is rejected.
	noSecretResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections/"+created.ID.String()+"/base-url", raw,
		`{"base_url":"https://gitlab2.example.com"}`))
	if err != nil {
		t.Fatalf("base-url without secret: %v", err)
	}
	if noSecretResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (base_url change without a new secret must be rejected)", noSecretResp.StatusCode)
	}

	okResp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections/"+created.ID.String()+"/base-url", raw,
		`{"base_url":"https://gitlab2.example.com","secret":"glpat-new-secret"}`))
	if err != nil {
		t.Fatalf("base-url with secret: %v", err)
	}
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", okResp.StatusCode)
	}

	var auditCount int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&auditCount, `SELECT count(*) FROM audit_events WHERE target_id = $1 AND action = 'connection.base_url_changed'`, created.ID); err != nil {
			t.Fatalf("count audit events: %v", err)
		}
	})
	if auditCount != 1 {
		t.Fatalf("audit event count = %d, want 1", auditCount)
	}
}

func TestConnections_SSRFAddressesRejectedAtCreate(t *testing.T) {
	app, db, cfg := newConnectionsTestApp(t)
	orgID, userID := seedMember(t, db, "acme", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme." + cfg.BaseDomain

	for _, baseURL := range []string{
		"https://169.254.169.254/",
		"http://localhost:8080",
	} {
		body := `{"provider":"gitlab","name":"n` + baseURL + `","base_url":"` + baseURL + `","secret":"s"}`
		resp, err := app.Test(jsonReqWithSession(http.MethodPost, host, "/connections", raw, body))
		if err != nil {
			t.Fatalf("create %s: %v", baseURL, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("base_url=%s status = %d, want 400", baseURL, resp.StatusCode)
		}
	}
	_ = orgID
}

func mustReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}
