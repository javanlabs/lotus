package authn_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
)

// fakeGitHub emulates just enough of GitHub's OAuth + REST API for the
// handler tests below: POST /login/oauth/access_token, GET /user, GET
// /user/emails.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gh-fake-token"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 42})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"email": "octocat@example.com", "primary": true, "verified": true},
		})
	})
	return httptest.NewServer(mux)
}

func newGitHubTestApp(t *testing.T) (*fiber.App, *sqlx.DB) {
	t.Helper()
	db := dbtest.Open(t)
	gh := fakeGitHub(t)
	t.Cleanup(gh.Close)

	p := authn.NewGitHubProvider("client-id", "client-secret", "http://auth.test/login/github/callback")
	p.AuthBaseURL = gh.URL + "/login/oauth/authorize"
	p.TokenURL = gh.URL + "/login/oauth/access_token"
	p.APIBaseURL = gh.URL

	app := fiber.New()
	registry := authn.Registry{"github": p}
	deps := authn.Deps{DB: db, OAuthProviders: registry, CookieSecure: false}
	app.Get("/login/:provider", authn.Login(deps))
	app.Get("/login/:provider/callback", authn.Callback(deps))
	return app, db
}

func TestOAuthFlow_HappyPath(t *testing.T) {
	t.Parallel()
	app, db := newGitHubTestApp(t)

	loginReq := httptest.NewRequest(http.MethodGet, "/login/github", nil)
	loginResp, err := app.Test(loginReq, -1)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	if loginResp.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d, want 302", loginResp.StatusCode)
	}
	state, cookies := extractStateAndCookies(t, loginResp)

	callbackReq := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		callbackReq.AddCookie(c)
	}
	callbackResp, err := app.Test(callbackReq, -1)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	if callbackResp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(callbackResp.Body)
		t.Fatalf("callback status = %d, want 302: %s", callbackResp.StatusCode, body)
	}

	var sessionCookieSet bool
	for _, c := range callbackResp.Cookies() {
		if c.Name == "lotus_auth" {
			sessionCookieSet = true
		}
	}
	if !sessionCookieSet {
		t.Fatal("expected lotus_auth session cookie to be set")
	}

	var userCount int
	if err := db.Get(&userCount, `SELECT count(*) FROM users`); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("users = %d, want 1", userCount)
	}
}

func TestOAuthFlow_StateReplayRejected(t *testing.T) {
	t.Parallel()
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, cookies := extractStateAndCookies(t, loginResp)

	req1 := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req1.AddCookie(c)
	}
	resp1, _ := app.Test(req1, -1)
	if resp1.StatusCode != http.StatusFound {
		t.Fatalf("first use status = %d, want 302", resp1.StatusCode)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	resp2, _ := app.Test(req2, -1)
	assertSentBackToLogin(t, resp2, "/", authn.NoticeLoginExpired)
}

func TestOAuthFlow_StateWithoutMatchingCookieRejected(t *testing.T) {
	t.Parallel()
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, _ := extractStateAndCookies(t, loginResp)

	// No cookie attached at all — simulates a leaked `state` param (e.g.
	// via Referer) used from a different browser/session.
	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	resp, _ := app.Test(req, -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginExpired)
}

func TestOAuthFlow_MismatchedCookieRejected(t *testing.T) {
	t.Parallel()
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, cookies := extractStateAndCookies(t, loginResp)
	_ = state

	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state=some-other-state-entirely", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, _ := app.Test(req, -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginExpired)
}

func extractStateAndCookies(t *testing.T, resp *http.Response) (string, []*http.Cookie) {
	t.Helper()
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("no redirect location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect URL")
	}
	return state, resp.Cookies()
}

// A rejected callback now answers with a redirect too, so "was it a 302"
// no longer tells success from failure. What does: where it points, and
// that no session cookie came with it.
func assertSentBackToLogin(t *testing.T, resp *http.Response, landing, notice string) {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want a 302 back to sign-in: %s", resp.StatusCode, body)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("no Location: %v", err)
	}
	if loc.Path != landing || loc.Query().Get("notice") != notice {
		t.Fatalf("redirected to %q, want %s?notice=%s", loc.String(), landing, notice)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "lotus_auth" && c.Value != "" {
			t.Fatal("a rejected callback set a session cookie")
		}
	}
}

// The production incident: a Keycloak page left open past the state
// cookie's ten minutes comes back without the cookie. It used to render
// "invalid or missing oauth state" as the whole page.
func TestOAuthFlow_ExpiredStateCookieSendsBackToSignInWithAReason(t *testing.T) {
	t.Parallel()
	app, db := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, _ := extractStateAndCookies(t, loginResp)

	// The browser has dropped the expired cookie; only the IdP's redirect
	// with its state parameter arrives.
	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil), -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginExpired)

	var users int
	if err := db.Get(&users, `SELECT count(*) FROM users`); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 0 {
		t.Fatalf("users = %d after a rejected callback, want 0", users)
	}
}

func TestOAuthFlow_CancelAtTheProviderSendsBackToSignIn(t *testing.T) {
	t.Parallel()
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, cookies := extractStateAndCookies(t, loginResp)

	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?error=access_denied&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, _ := app.Test(req, -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginCancelled)
}

func TestOAuthFlow_ProviderErrorOtherThanCancelIsAFailure(t *testing.T) {
	t.Parallel()
	app, _ := newGitHubTestApp(t)

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github/callback?error=invalid_client", nil), -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginFailed)
}

func TestOAuthFlow_FailedTokenExchangeSendsBackToSignInAndKeepsTheReturnPath(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(broken.Close)

	p := authn.NewGitHubProvider("client-id", "wrong-secret", "http://auth.test/login/github/callback")
	p.AuthBaseURL = broken.URL + "/login/oauth/authorize"
	p.TokenURL = broken.URL + "/login/oauth/access_token"
	p.APIBaseURL = broken.URL

	app := fiber.New()
	deps := authn.Deps{DB: db, OAuthProviders: authn.Registry{"github": p}}
	app.Get("/login/:provider", authn.Login(deps))
	app.Get("/login/:provider/callback", authn.Callback(deps))

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github?return=/projects/abc", nil), -1)
	state, cookies := extractStateAndCookies(t, loginResp)
	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, _ := app.Test(req, -1)
	assertSentBackToLogin(t, resp, "/", authn.NoticeLoginFailed)

	loc, _ := resp.Location()
	if got := loc.Query().Get("return"); got != "/projects/abc" {
		t.Errorf("return = %q, want the page the person was originally heading to", got)
	}
}

func TestLoginNoticeIgnoresUnknownCodes(t *testing.T) {
	t.Parallel()
	// The code is looked up, never echoed, so a crafted link cannot put
	// arbitrary text on the login page.
	for _, code := range []string{"", "nope", "<script>alert(1)</script>"} {
		if got := authn.LoginNotice(code); got != "" {
			t.Errorf("LoginNotice(%q) = %q, want empty", code, got)
		}
	}
	for _, code := range []string{authn.NoticeLoginExpired, authn.NoticeLoginCancelled, authn.NoticeLoginFailed, authn.NoticeOwnerPending} {
		if authn.LoginNotice(code) == "" {
			t.Errorf("LoginNotice(%q) has no message", code)
		}
	}
}

// The other message that was a bare page on production. In single mode a
// verified login that is not the nominated Owner goes back to /login and
// is told why.
func TestOAuthFlow_NotTheNominatedOwnerSendsBackToLoginInSingleMode(t *testing.T) {
	t.Parallel()
	db, _ := seedBootstrap(t, "someone-else@javan.co.id")
	gh := fakeGitHub(t) // signs in as octocat@example.com, verified
	t.Cleanup(gh.Close)

	p := authn.NewGitHubProvider("client-id", "client-secret", "http://lotus.test/login/github/callback")
	p.AuthBaseURL = gh.URL + "/login/oauth/authorize"
	p.TokenURL = gh.URL + "/login/oauth/access_token"
	p.APIBaseURL = gh.URL

	app := fiber.New()
	deps := authn.Deps{
		DB:             db,
		OAuthProviders: authn.Registry{"github": p},
		Cfg:            config.Config{Mode: config.ModeSingle, URL: "http://lotus.test"},
	}
	app.Get("/login/:provider", authn.Login(deps))
	app.Get("/login/:provider/callback", authn.Callback(deps))

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil), -1)
	state, cookies := extractStateAndCookies(t, loginResp)
	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, _ := app.Test(req, -1)
	assertSentBackToLogin(t, resp, "/login", authn.NoticeOwnerPending)
}
