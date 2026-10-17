package authn_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/tenant"
)

const handoffTestDomain = "lotus.test"

func newHandoffTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	renderer := testRenderer(t)

	deps := authn.Deps{DB: db, Cfg: cfg, Renderer: renderer, OAuthProviders: authn.Registry{}, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/handoff/start", tenant.RequireTenant(), authn.HandoffStart(deps))
	app.Get("/handoff/authorize", tenant.RequireAuth(), authn.HandoffAuthorize(deps))
	app.Get("/handoff/finish", tenant.RequireTenant(), authn.HandoffFinish(deps))
	return app, db, cfg
}

func hostReq(method, host, path string) *http.Request {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	return req
}

// hostFormReq builds a POST request with an application/x-www-form-
// urlencoded body. httptest.NewRequest must receive the body reader at
// construction time — swapping req.Body in afterward leaves
// Content-Length at 0 from the nil-body construction, and the server then
// reads no body at all (or errors trying).
func hostFormReq(host, path, form string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(form))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// seedActiveMember creates an org, a user, and an active membership,
// returning both ids.
func seedActiveMember(t *testing.T, db *sqlx.DB, subdomain string) (orgID, userID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, subdomain+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var roleID uuid.UUID
	if err := db.Get(&roleID, `SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		t.Fatalf("lookup owner role: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, roleID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return orgID, userID
}

func seedGlobalSession(t *testing.T, db *sqlx.DB, userID uuid.UUID) string {
	t.Helper()
	raw := "test-global-session-" + userID.String()
	_, err := db.Exec(`INSERT INTO sessions (id, user_id, org_id, host, expires_at) VALUES ($1, $2, NULL, 'auth.test', now() + interval '1 day')`,
		sha256HexForTest(raw), userID)
	if err != nil {
		t.Fatalf("insert global session: %v", err)
	}
	return raw
}

func TestHandoff_FullFlow(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	orgID, userID := seedActiveMember(t, db, "acme")
	_ = orgID
	sessionRaw := seedGlobalSession(t, db, userID)

	// Step 1: start on the tenant host.
	startResp, err := app.Test(hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/handoff/start?return=/dashboard"), -1)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if startResp.StatusCode != http.StatusFound {
		t.Fatalf("start status = %d, want 302", startResp.StatusCode)
	}
	authorizeURL, err := startResp.Location()
	if err != nil {
		t.Fatalf("no redirect location: %v", err)
	}
	var hsCookie *http.Cookie
	for _, c := range startResp.Cookies() {
		if c.Name == "lotus_hs" {
			hsCookie = c
		}
	}
	if hsCookie == nil {
		t.Fatal("expected lotus_hs cookie to be set")
	}

	// Step 2/3: authorize on the auth host, with a global session cookie.
	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	authResp, err := app.Test(authReq, -1)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if authResp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", authResp.StatusCode)
	}
	finishURL, err := authResp.Location()
	if err != nil {
		t.Fatalf("no redirect location from authorize: %v", err)
	}

	// Step 4: finish on the tenant host, presenting the verifier cookie.
	finishReq := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, finishURL.RequestURI())
	finishReq.AddCookie(hsCookie)
	finishResp, err := app.Test(finishReq, -1)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finishResp.StatusCode != http.StatusFound {
		t.Fatalf("finish status = %d, want 302", finishResp.StatusCode)
	}
	if loc, _ := finishResp.Location(); loc.Path != "/dashboard" {
		t.Fatalf("finish redirected to %q, want /dashboard", loc)
	}
	if finishResp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("expected Referrer-Policy: no-referrer on /handoff/finish")
	}

	var sessionCookieSet bool
	for _, c := range finishResp.Cookies() {
		if c.Name == "lotus_session" {
			sessionCookieSet = true
		}
	}
	if !sessionCookieSet {
		t.Fatal("expected lotus_session cookie to be set")
	}
}

func TestHandoff_CodeReplayRejected(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	_, userID := seedActiveMember(t, db, "acme")
	sessionRaw := seedGlobalSession(t, db, userID)

	startResp, _ := app.Test(hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/handoff/start"), -1)
	authorizeURL, _ := startResp.Location()
	var hsCookie *http.Cookie
	for _, c := range startResp.Cookies() {
		if c.Name == "lotus_hs" {
			hsCookie = c
		}
	}

	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	authResp, _ := app.Test(authReq, -1)
	finishURL, _ := authResp.Location()

	finishReq1 := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, finishURL.RequestURI())
	finishReq1.AddCookie(hsCookie)
	resp1, _ := app.Test(finishReq1, -1)
	if resp1.StatusCode != http.StatusFound {
		t.Fatalf("first finish status = %d, want 302", resp1.StatusCode)
	}

	finishReq2 := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, finishURL.RequestURI())
	finishReq2.AddCookie(hsCookie)
	resp2, _ := app.Test(finishReq2, -1)
	if resp2.StatusCode == http.StatusFound {
		t.Fatal("expected replayed code to be rejected")
	}
}

func TestHandoff_CrossOrgCodeRejected(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	_, userIDA := seedActiveMember(t, db, "acme")
	_, _ = seedActiveMember(t, db, "beta")
	sessionRaw := seedGlobalSession(t, db, userIDA)

	startResp, _ := app.Test(hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/handoff/start"), -1)
	authorizeURL, _ := startResp.Location()

	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	authResp, _ := app.Test(authReq, -1)
	finishURL, _ := authResp.Location()

	var hsCookie *http.Cookie
	for _, c := range startResp.Cookies() {
		if c.Name == "lotus_hs" {
			hsCookie = c
		}
	}

	// Present the code minted for "acme" on "beta"'s host instead.
	finishReq := hostReq(http.MethodGet, "beta."+cfg.BaseDomain, finishURL.RequestURI())
	finishReq.AddCookie(hsCookie)
	resp, err := app.Test(finishReq, -1)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if resp.StatusCode == http.StatusFound {
		t.Fatal("expected code minted for a different org to be rejected")
	}
}

func TestHandoff_MissingVerifierCookieRejected(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	_, userID := seedActiveMember(t, db, "acme")
	sessionRaw := seedGlobalSession(t, db, userID)

	startResp, _ := app.Test(hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/handoff/start"), -1)
	authorizeURL, _ := startResp.Location()

	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	authResp, _ := app.Test(authReq, -1)
	finishURL, _ := authResp.Location()

	// No verifier cookie attached at all.
	finishReq := hostReq(http.MethodGet, "acme."+cfg.BaseDomain, finishURL.RequestURI())
	resp, err := app.Test(finishReq, -1)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if resp.StatusCode == http.StatusFound {
		t.Fatal("expected missing verifier cookie to be rejected")
	}
}

func TestHandoff_NonMemberGetsFriendlyPage(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	// User exists but is not a member of "acme".
	if _, err := db.Exec(`INSERT INTO organizations (subdomain, name) VALUES ('acme', 'acme')`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('outsider@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	sessionRaw := seedGlobalSession(t, db, userID)

	startResp, _ := app.Test(hostReq(http.MethodGet, "acme."+cfg.BaseDomain, "/handoff/start"), -1)
	authorizeURL, _ := startResp.Location()

	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	authResp, err := app.Test(authReq, -1)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("non-member authorize status = %d, want 200 (friendly page, not 302/500)", authResp.StatusCode)
	}
}

func TestHandoff_ExpiredHandshakeRejected(t *testing.T) {
	t.Parallel()
	app, db, cfg := newHandoffTestApp(t)
	_, userID := seedActiveMember(t, db, "acme")
	sessionRaw := seedGlobalSession(t, db, userID)

	var orgID uuid.UUID
	if err := db.Get(&orgID, `SELECT id FROM organizations WHERE subdomain = 'acme'`); err != nil {
		t.Fatalf("lookup org: %v", err)
	}
	var hsID uuid.UUID
	err := db.Get(&hsID, `
		INSERT INTO handoff_handshakes (org_id, return_path, verifier_hash, expires_at)
		VALUES ($1, '/', 'deadbeef', now() - interval '1 second')
		RETURNING id`, orgID)
	if err != nil {
		t.Fatalf("insert expired handshake: %v", err)
	}

	authReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/handoff/authorize?hs="+hsID.String())
	authReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	resp, err := app.Test(authReq, -1)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expired handshake status = %d, want 200 (friendly page)", resp.StatusCode)
	}
}
