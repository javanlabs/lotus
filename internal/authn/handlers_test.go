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
	app, db := newGitHubTestApp(t)

	loginReq := httptest.NewRequest(http.MethodGet, "/login/github", nil)
	loginResp, err := app.Test(loginReq)
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
	callbackResp, err := app.Test(callbackReq)
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
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil))
	state, cookies := extractStateAndCookies(t, loginResp)

	req1 := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req1.AddCookie(c)
	}
	resp1, _ := app.Test(req1)
	if resp1.StatusCode != http.StatusFound {
		t.Fatalf("first use status = %d, want 302", resp1.StatusCode)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	resp2, _ := app.Test(req2)
	if resp2.StatusCode == http.StatusFound {
		t.Fatal("expected reused state to be rejected, got a redirect (success)")
	}
}

func TestOAuthFlow_StateWithoutMatchingCookieRejected(t *testing.T) {
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil))
	state, _ := extractStateAndCookies(t, loginResp)

	// No cookie attached at all — simulates a leaked `state` param (e.g.
	// via Referer) used from a different browser/session.
	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state="+state, nil)
	resp, _ := app.Test(req)
	if resp.StatusCode == http.StatusFound {
		t.Fatal("expected state without cookie to be rejected")
	}
}

func TestOAuthFlow_MismatchedCookieRejected(t *testing.T) {
	app, _ := newGitHubTestApp(t)

	loginResp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/login/github", nil))
	state, cookies := extractStateAndCookies(t, loginResp)
	_ = state

	req := httptest.NewRequest(http.MethodGet, "/login/github/callback?code=abc&state=some-other-state-entirely", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, _ := app.Test(req)
	if resp.StatusCode == http.StatusFound {
		t.Fatal("expected mismatched state/cookie pair to be rejected")
	}
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
