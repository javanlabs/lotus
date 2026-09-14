package authn_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/tenant"
)

// newSingleModeLoginApp wires the single-mode routes the production loop
// ran through: OAuth login and callback, a page behind the tenant session
// check, and the invitation landing. The fake GitHub signs everyone in as
// octocat@example.com with a verified address.
func newSingleModeLoginApp(t *testing.T, db *sqlx.DB) *fiber.App {
	t.Helper()
	gh := fakeGitHub(t)
	t.Cleanup(gh.Close)

	p := authn.NewGitHubProvider("client-id", "client-secret", "http://lotus.test/login/github/callback")
	p.AuthBaseURL = gh.URL + "/login/oauth/authorize"
	p.TokenURL = gh.URL + "/login/oauth/access_token"
	p.APIBaseURL = gh.URL

	cfg := config.Config{Mode: config.ModeSingle, URL: "http://lotus.test"}
	deps := authn.Deps{
		DB:             db,
		Cfg:            cfg,
		Renderer:       testRenderer(t),
		OAuthProviders: authn.Registry{"github": p},
	}

	app := fiber.New()
	app.Use(tenant.NewResolver(cfg, db).Middleware())
	app.Get("/login/:provider", authn.Login(deps))
	app.Get("/login/:provider/callback", authn.Callback(deps))
	app.Get("/projects", authn.RequireTenantSession(deps), func(c *fiber.Ctx) error { return c.SendString("projects") })
	app.Get("/invite/:token", authn.InviteLanding(deps))
	return app
}

// signInThroughGitHub runs the browser's half of an OAuth round trip and
// returns the callback response.
func signInThroughGitHub(t *testing.T, app *fiber.App, returnPath string) *http.Response {
	t.Helper()
	loginResp, err := app.Test(httptest.NewRequest(http.MethodGet, "/login/github?return="+returnPath, nil))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	state, cookies := extractStateAndCookies(t, loginResp)
	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	return resp
}

func tenantSessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "lotus_session" && c.Value != "" {
			return c
		}
	}
	return nil
}

// The production loop, replayed. The nominated Owner signed in through
// Keycloak, the claim succeeded, and /projects sent them straight back to
// /login — over and over — because the callback minted only the global
// session and single mode has no handoff to mint the tenant one.
func TestSingleMode_OAuthLoginOpensTheAppInsteadOfLoopingBackToLogin(t *testing.T) {
	db, _ := seedBootstrap(t, "octocat@example.com")
	app := newSingleModeLoginApp(t, db)

	resp := signInThroughGitHub(t, app, "/projects")
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback status = %d: %s", resp.StatusCode, body)
	}
	if loc, _ := resp.Location(); loc == nil || loc.Path != "/projects" {
		t.Fatalf("callback redirected to %v, want /projects", loc)
	}
	session := tenantSessionCookie(resp)
	if session == nil {
		t.Fatal("callback set no lotus_session cookie; /projects will bounce back to /login")
	}

	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	req.AddCookie(session)
	page, err := app.Test(req)
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	if page.StatusCode != http.StatusOK {
		loc, _ := page.Location()
		t.Fatalf("/projects status = %d (location %v), want 200 — the login loop is back", page.StatusCode, loc)
	}
}

// Someone who signs in but belongs to no organization would otherwise
// loop the same way. They are told instead, and given no tenant session.
func TestSingleMode_OAuthLoginByANonMemberIsToldRatherThanLooped(t *testing.T) {
	db := openOrgWithoutBootstrap(t)
	app := newSingleModeLoginApp(t, db)

	resp := signInThroughGitHub(t, app, "/projects")
	loc, err := resp.Location()
	if resp.StatusCode != http.StatusFound || err != nil || loc.Path != "/login" || loc.Query().Get("notice") != authn.NoticeNoAccess {
		t.Fatalf("status %d location %v, want /login?notice=%s", resp.StatusCode, loc, authn.NoticeNoAccess)
	}
	// Unlike a rejected callback, this sign-in succeeded: the identity is
	// real, only membership is missing. The global session is kept on
	// purpose so an invitation link can recognise them later; what they
	// must not get is the tenant session that opens the app.
	if tenantSessionCookie(resp) != nil {
		t.Fatal("a non-member was given a tenant session")
	}
}

// An invitee signs in before they are a member. Their invitation link has
// to recognise them afterwards, so they go back to it — not to a notice.
func TestSingleMode_OAuthLoginByAnInviteeReturnsToTheInvitation(t *testing.T) {
	db := openOrgWithoutBootstrap(t)
	app := newSingleModeLoginApp(t, db)

	resp := signInThroughGitHub(t, app, "/invite/some-token")
	loc, err := resp.Location()
	if err != nil || loc.Path != "/invite/some-token" {
		t.Fatalf("redirected to %v, want back to the invitation", loc)
	}
}

// Accepting an invitation in single mode pointed the browser at
// <subdomain>.<base>/handoff/start — a saas-only host that does not exist
// on a single-origin install. It now lands in the app with a session.
func TestSingleMode_AcceptingAnInvitationLandsInTheApp(t *testing.T) {
	db := openOrgWithoutBootstrap(t)
	app := newSingleModeLoginApp(t, db)

	orgID, ownerID := seedInvitedOrgInto(t, db)
	_, token := createTestInvitation(t, db, orgID, ownerID, "alice@example.com", "member", 24*time.Hour)
	var aliceID uuid.UUID
	if err := db.Get(&aliceID, `INSERT INTO users (email, email_verified_at) VALUES ('alice@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert invitee: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/invite/"+token, nil)
	req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: seedGlobalSession(t, db, aliceID)})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	loc, _ := resp.Location()
	if resp.StatusCode != http.StatusFound || loc == nil || loc.Host != "" || loc.Path != "/" {
		t.Fatalf("status %d location %v, want a same-origin redirect to /", resp.StatusCode, loc)
	}
	if tenantSessionCookie(resp) == nil {
		t.Fatal("accepted invitee got no tenant session")
	}
}

// openOrgWithoutBootstrap is a single-mode instance whose Owner seat is
// already settled: one organization, nothing pending.
func openOrgWithoutBootstrap(t *testing.T) *sqlx.DB {
	t.Helper()
	db, orgID := seedBootstrap(t, "owner@javan.co.id")
	if _, err := db.Exec(`UPDATE instance_bootstrap SET consumed_at = now() WHERE org_id = $1`, orgID); err != nil {
		t.Fatalf("settle bootstrap: %v", err)
	}
	return db
}

// seedInvitedOrgInto returns the single organization and an Owner to act
// as the inviter.
func seedInvitedOrgInto(t *testing.T, db *sqlx.DB) (orgID, ownerID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `SELECT id FROM organizations LIMIT 1`); err != nil {
		t.Fatalf("org: %v", err)
	}
	if err := db.Get(&ownerID, `INSERT INTO users (email, email_verified_at) VALUES ('owner@javan.co.id', now()) RETURNING id`); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id)
		SELECT $1, $2, id FROM roles WHERE key = 'owner' AND org_id IS NULL`, orgID, ownerID); err != nil {
		t.Fatalf("owner membership: %v", err)
	}
	return orgID, ownerID
}
