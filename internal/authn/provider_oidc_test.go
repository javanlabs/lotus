package authn_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/uyab/lotus/internal/authn"
)

// fakeIdP is a minimal OIDC issuer: a discovery document pointing at its
// own three endpoints. discoveryHits counts how often the well-known
// document is fetched, which is how the caching test observes the cache.
type fakeIdP struct {
	server        *httptest.Server
	discoveryHits int32
	userinfo      map[string]any
	discoveryCode int
	discoveryBody string
}

func newFakeIdP(t *testing.T, userinfo map[string]any) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{userinfo: userinfo}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&idp.discoveryHits, 1)
		if idp.discoveryCode != 0 {
			w.WriteHeader(idp.discoveryCode)
			return
		}
		if idp.discoveryBody != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(idp.discoveryBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 idp.server.URL,
			"authorization_endpoint": idp.server.URL + "/protocol/openid-connect/auth",
			"token_endpoint":         idp.server.URL + "/protocol/openid-connect/token",
			"userinfo_endpoint":      idp.server.URL + "/protocol/openid-connect/userinfo",
		})
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code_verifier") == "" || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-123"}`))
	})
	mux.HandleFunc("/protocol/openid-connect/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(idp.userinfo)
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func TestOIDCProviderDiscoversItsEndpointsAndBuildsAnAuthURL(t *testing.T) {
	idp := newFakeIdP(t, nil)
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "https://lotus.example/login/sso/callback")

	got, err := p.AuthCodeURL("state-abc", "challenge-xyz")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if !strings.HasSuffix(u.Path, "/protocol/openid-connect/auth") {
		t.Errorf("auth path = %q, want the discovered authorization_endpoint", u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":             "lotus",
		"redirect_uri":          "https://lotus.example/login/sso/callback",
		"response_type":         "code",
		"state":                 "state-abc",
		"code_challenge":        "challenge-xyz",
		"code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	// Without the openid scope an OIDC issuer has no reason to return a
	// subject or an email, which is the entire point of using one.
	if !strings.Contains(q.Get("scope"), "openid") || !strings.Contains(q.Get("scope"), "email") {
		t.Errorf("scope = %q, want openid and email", q.Get("scope"))
	}
}

func TestOIDCProviderDiscoversOnceAcrossManyCalls(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{"sub": "u-1", "email": "a@b.c", "email_verified": true})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "https://lotus.example/login/sso/callback")

	if _, err := p.AuthCodeURL("s", "c"); err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	if _, err := p.Exchange(context.Background(), "code", "verifier"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if _, err := p.FetchIdentity(context.Background(), "at-123"); err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if n := atomic.LoadInt32(&idp.discoveryHits); n != 1 {
		t.Errorf("discovery fetched %d times across three calls, want 1", n)
	}
}

func TestOIDCProviderReturnsTheStandardClaims(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{
		"sub": "kc-9f2", "email": "bayu@javan.co.id", "email_verified": true, "name": "Bayu Hendra",
	})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "https://lotus.example/login/sso/callback")

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.ProviderSub != "kc-9f2" || id.Email != "bayu@javan.co.id" || id.Name != "Bayu Hendra" {
		t.Errorf("identity = %+v", id)
	}
	// Load-bearing: ClaimOwnershipIfPending refuses to hand a fresh
	// single-mode instance to an unverified address.
	if !id.EmailVerified {
		t.Error("EmailVerified = false, want the issuer's claim to carry through")
	}
}

func TestOIDCProviderFallsBackToPreferredUsernameForTheName(t *testing.T) {
	// Keycloak omits `name` unless both given and family name are set.
	idp := newFakeIdP(t, map[string]any{
		"sub": "kc-1", "email": "a@b.c", "email_verified": true, "preferred_username": "bayu",
	})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.Name != "bayu" {
		t.Errorf("Name = %q, want the preferred_username fallback", id.Name)
	}
}

func TestOIDCProviderCarriesAnUnverifiedEmailThrough(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{"sub": "kc-2", "email": "a@b.c", "email_verified": false})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.EmailVerified {
		t.Error("EmailVerified = true for an issuer that said false — ownership claim would be forgeable")
	}
}

func TestOIDCProviderErrorsRatherThanRedirectingNowhereWhenDiscoveryFails(t *testing.T) {
	// The reason Provider.AuthCodeURL returns an error at all: an
	// unreachable issuer must fail the login, not send the browser to "".
	idp := newFakeIdP(t, nil)
	idp.discoveryCode = http.StatusInternalServerError
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	got, err := p.AuthCodeURL("s", "c")
	if err == nil {
		t.Fatalf("AuthCodeURL = %q, want an error when discovery fails", got)
	}
	if got != "" {
		t.Errorf("AuthCodeURL = %q on failure, want empty", got)
	}
}

func TestOIDCProviderRejectsADiscoveryDocumentMissingAnEndpoint(t *testing.T) {
	idp := newFakeIdP(t, nil)
	idp.discoveryBody = `{"issuer":"x","authorization_endpoint":"https://idp/auth"}`
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	if _, err := p.AuthCodeURL("s", "c"); err == nil {
		t.Fatal("want an error for a document with no token or userinfo endpoint")
	}
}

func TestOIDCProviderRetriesDiscoveryAfterAFailure(t *testing.T) {
	// A failed discovery must not be cached: the IdP is the only way into
	// a single-mode instance, so a blip during startup cannot lock
	// everyone out until someone thinks to restart the process.
	idp := newFakeIdP(t, nil)
	idp.discoveryCode = http.StatusBadGateway
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	if _, err := p.AuthCodeURL("s", "c"); err == nil {
		t.Fatal("want the first attempt to fail")
	}
	idp.discoveryCode = 0
	if _, err := p.AuthCodeURL("s", "c"); err != nil {
		t.Fatalf("second attempt after the IdP recovered: %v", err)
	}
}

func TestOIDCProviderLeavesAnUnverifiedEmailUnverifiedByDefault(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{"sub": "kc-3", "email": "bayu@javan.co.id", "email_verified": false})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.EmailVerified {
		t.Error("EmailVerified = true without TrustEmail — the default must be to believe the issuer's claim")
	}
}

func TestOIDCProviderTrustEmailVouchesForAnAdministeredAddress(t *testing.T) {
	// Javan's Keycloak reports email_verified=false for every account,
	// because an administrator creates them and nobody clicks a
	// verification link. That locked the first Owner out of production.
	idp := newFakeIdP(t, map[string]any{"sub": "kc-4", "email": "bayu@javan.co.id", "email_verified": false})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")
	p.TrustEmail = true

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if !id.EmailVerified {
		t.Error("EmailVerified = false with TrustEmail on")
	}
}

func TestOIDCProviderTrustEmailNeverVerifiesAnEmptyAddress(t *testing.T) {
	idp := newFakeIdP(t, map[string]any{"sub": "kc-5", "email": "", "email_verified": false})
	p := authn.NewOIDCProvider(idp.server.URL, "lotus", "shh", "cb")
	p.TrustEmail = true

	id, err := p.FetchIdentity(context.Background(), "at-123")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.EmailVerified {
		t.Error("an empty address came back verified; there is nothing to vouch for")
	}
}
