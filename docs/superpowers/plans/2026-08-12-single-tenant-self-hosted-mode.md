# Single-Tenant Self-Hosted Mode + Generic OIDC — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one Lotus codebase install three ways — self-hosted, local, and multi-tenant SaaS — by collapsing the three-host taxonomy into one origin holding exactly one organization, with Keycloak/OIDC login configured from environment variables.

**Architecture:** A `LOTUS_MODE=saas|single` switch (defaulting to `saas`, so nothing deployed changes). In `single` mode the subdomain resolver skips host classification entirely and always answers with the one organization; `RequireAuth()` and `RequireTenant()` both pass, driven by a new `Resolved.Single` flag rather than a bitmask enum. SaaS-only routes (handoff, signup, onboarding) are not registered at all in single mode. A generic OIDC provider implements the existing `authn.Provider` interface and is discovered from `OIDC_ISSUER`. The org is created ahead of first boot by `lotus init`, which leaves ownership pending in a one-row `instance_bootstrap` table until the first verified matching login claims it.

**Tech Stack:** Go 1.x, Fiber v2, sqlx, PostgreSQL (goose-style numbered SQL migrations), html/template + htmx + Alpine + Tailwind v4, Playwright for e2e, Docker Compose for Keycloak in CI.

**Design doc:** `docs/superpowers/specs/2026-08-12-single-tenant-self-hosted-mode-design.md` — read it before starting. Section references below (§4.2, §4.5, …) point into it.

## Global Constraints

- **Commits require explicit user approval.** This repo's standing rule is that Claude never commits unless the user asks. The `Commit` step at the end of each task is written out for completeness, but the executor must get an explicit go-ahead before running it. Do not batch-commit silently.
- **Default mode is `saas`.** Every change must leave the existing multi-tenant behavior byte-identical when `LOTUS_MODE` is unset. Tasks 1–3 ship dark.
- **Every new route needs an `authz.Require(...)` or `authz.Public("reason")` marker.** `TestRouteCoverage` fails the build otherwise. This is non-negotiable and is the single most important guardrail in this plan.
- **SonarLint-clean.** No unused variables, no duplicated blocks, no cognitive-complexity spikes. Match the surrounding code's comment density — this codebase explains *why*, not *what*.
- **Every task adds tests.** Go tests live beside the code (`foo_test.go`); e2e tests live in `e2e/tests/`.
- **Document every new env var in `README.md`'s env table.** That table is the contract.
- **Migrations are append-only** and numbered sequentially. The next free number is `00021`.
- **Record results in ActiveCollab project 697.** Each task below names the AC task it reports to. Post an implementation-report comment on completion and set the task's label to `READY FOR TEST` — as a real label, never as a title prefix.

## File Structure

**Created:**
| File | Responsibility |
|---|---|
| `internal/config/config_test.go` | Mode/URL parsing and validation table tests |
| `internal/authn/provider_oidc.go` | Generic OIDC provider + discovery |
| `internal/authn/provider_oidc_test.go` | Provider tests against an `httptest.Server` |
| `internal/authn/bootstrap.go` | First-login ownership claim |
| `internal/authn/bootstrap_test.go` | Claim/reject/idempotency tests |
| `internal/repo/global/bootstrap.go` | `instance_bootstrap` + single-org queries |
| `internal/db/migrations/00021_instance_bootstrap.sql` | The bootstrap table |
| `cmd/lotus/init.go` | The `lotus init` subcommand |
| `e2e/tests/single-mode.spec.ts` | Single-mode e2e |
| `docker-compose.yml` | Postgres + Keycloak for local/CI |
| `deploy/keycloak/realm-lotus.json` | Importable Keycloak realm |

**Modified:**
| File | Change |
|---|---|
| `internal/config/config.go` | `Mode`, `URL`, `IsSingle()`, `BaseURL()`, `Validate()`; mode assertions on `AuthHost()`/`TenantHost()` |
| `internal/tenant/resolve.go` | `Resolved.Single`; single-mode branch in `classify`; mode-aware guards |
| `internal/tenant/resolve_test.go` | Single-mode resolution tests |
| `internal/web/server.go` | Route table split into shared vs SaaS-only |
| `internal/web/route_coverage_test.go` | Parameterize over both modes |
| `cmd/lotus/main.go` | `init` subcommand; `cfg.Validate()`; OIDC in `oauthProviders` |
| `README.md` | New env vars + a self-hosted install section |

---

### Task 1: Deployment mode configuration

Adds `LOTUS_MODE` and `LOTUS_URL` with validation. Pure plumbing — no behavior changes at the default. Everything downstream reads these.

**AC task:** 4.8 (create it if absent — see Task 6).

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `cmd/lotus/main.go` (call `cfg.Validate()`)

**Interfaces:**
- Consumes: nothing.
- Produces: `config.ModeSaaS`/`config.ModeSingle` constants; `Config.Mode string`; `Config.URL string`; `Config.IsSingle() bool`; `Config.BaseURL() string`; `Config.Validate() error`. Tasks 2–5 all depend on `IsSingle()` and `BaseURL()`.

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/config"
)

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		cfg     config.Config
		wantErr string // substring; "" means valid
	}{
		"saas with base domain": {
			cfg: config.Config{Mode: config.ModeSaaS, BaseDomain: "lotus.com"},
		},
		"saas without base domain": {
			cfg:     config.Config{Mode: config.ModeSaaS},
			wantErr: "LOTUS_BASE_DOMAIN",
		},
		"single with url": {
			cfg: config.Config{Mode: config.ModeSingle, URL: "https://lotus.acme.internal"},
		},
		"single without url": {
			cfg:     config.Config{Mode: config.ModeSingle},
			wantErr: "LOTUS_URL",
		},
		"single with a bare host, not a URL": {
			cfg:     config.Config{Mode: config.ModeSingle, URL: "lotus.acme.internal"},
			wantErr: "absolute URL",
		},
		"unknown mode": {
			cfg:     config.Config{Mode: "cluster", BaseDomain: "lotus.com"},
			wantErr: "LOTUS_MODE",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	saas := config.Config{Mode: config.ModeSaaS, BaseDomain: "lotus.com", CookieSecure: true}
	if got, want := saas.BaseURL(), "https://auth.lotus.com"; got != want {
		t.Errorf("saas BaseURL() = %q, want %q", got, want)
	}
	// A trailing slash in LOTUS_URL must not produce "//login/oidc/callback".
	single := config.Config{Mode: config.ModeSingle, URL: "https://lotus.acme.internal/"}
	if got, want := single.BaseURL(), "https://lotus.acme.internal"; got != want {
		t.Errorf("single BaseURL() = %q, want %q", got, want)
	}
}

// TestHostHelpersPanicInSingleMode pins the mode assertion: AuthHost and
// TenantHost describe a subdomain layout that does not exist in single
// mode, so calling them there is a programming error that must surface as
// a test failure, not as a malformed link in production.
func TestHostHelpersPanicInSingleMode(t *testing.T) {
	single := config.Config{Mode: config.ModeSingle, URL: "https://lotus.acme.internal"}
	for name, call := range map[string]func(){
		"AuthHost":   func() { _ = single.AuthHost() },
		"TenantHost": func() { _ = single.TenantHost("acme") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s() did not panic in single mode", name)
				}
			}()
			call()
		})
	}
}

func TestIsSingle(t *testing.T) {
	if (config.Config{Mode: config.ModeSingle}).IsSingle() != true {
		t.Error("IsSingle() = false for ModeSingle")
	}
	if (config.Config{Mode: config.ModeSaaS}).IsSingle() != false {
		t.Error("IsSingle() = true for ModeSaaS")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run 'TestValidate|TestBaseURL|TestHostHelpers|TestIsSingle' -v`
Expected: FAIL — `undefined: config.ModeSaaS`, `cfg.Validate undefined`, `cfg.BaseURL undefined`.

- [ ] **Step 3: Add the fields, constants and methods**

In `internal/config/config.go`, add to the imports: `"errors"`, `"fmt"`, `"net/url"`. Add above `type Config struct`:

```go
// Mode is the deployment shape this process runs in. ModeSaaS is the
// multi-tenant product: three host kinds (apex / auth / tenant), wildcard
// DNS, one organization per subdomain. ModeSingle is a self-hosted or
// local install: one origin, exactly one organization, no subdomains and
// therefore no wildcard DNS or wildcard certificate to arrange.
//
// The default is deliberately ModeSaaS so that an existing deployment
// that sets no LOTUS_MODE keeps behaving exactly as before.
const (
	ModeSaaS   = "saas"
	ModeSingle = "single"
)
```

Add these fields to `Config`, after `BaseDomain`:

```go
	// Mode is ModeSaaS or ModeSingle; see the constants above.
	Mode string

	// URL is the absolute base URL of a single-mode install, e.g.
	// "https://lotus.acme.internal". It replaces BaseDomain there: with no
	// subdomains there is no domain to carve up, only one origin to build
	// absolute links (OAuth/OIDC redirect URIs) against. Unused in saas
	// mode, required in single mode.
	URL string
```

Add to `Load()`'s `Config{...}` literal:

```go
		Mode:               envOr("LOTUS_MODE", ModeSaaS),
		URL:                strings.TrimSuffix(os.Getenv("LOTUS_URL"), "/"),
```

(`strings` is already imported.)

Then replace the existing `AuthHost`/`TenantHost` one-liners with:

```go
// IsSingle reports whether this process runs as a single-organization
// install. It is the one predicate the resolver, the route table, and the
// provider registry all branch on.
func (c Config) IsSingle() bool { return c.Mode == ModeSingle }

// AuthHost and TenantHost describe the saas-mode subdomain layout. In
// single mode that layout does not exist, so calling either is a
// programming error rather than a recoverable condition — panicking turns
// it into a test failure instead of a malformed link shipped to a user.
func (c Config) AuthHost() string {
	if c.IsSingle() {
		panic("config: AuthHost is meaningless in single mode; use BaseURL")
	}
	return "auth." + c.BaseDomain
}

func (c Config) TenantHost(slug string) string {
	if c.IsSingle() {
		panic("config: TenantHost is meaningless in single mode; use BaseURL")
	}
	return slug + "." + c.BaseDomain
}

// BaseURL is the absolute origin that pre-session links are built against
// — OAuth and OIDC redirect URIs, and the links in invitation emails. In
// saas mode that is always the auth host, since every pre-session flow
// lives there; in single mode it is the install's one origin.
func (c Config) BaseURL() string {
	if c.IsSingle() {
		return c.URL
	}
	return c.Scheme() + "://" + c.AuthHost()
}

// Validate checks the combinations Load cannot: Load reads whatever is in
// the environment, Validate decides whether that combination can actually
// serve. Called once at startup so a misconfiguration fails the deploy
// rather than the first user's login.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeSaaS:
		if c.BaseDomain == "" {
			return errors.New("config: LOTUS_BASE_DOMAIN is required in saas mode")
		}
	case ModeSingle:
		if c.URL == "" {
			return errors.New("config: LOTUS_URL is required when LOTUS_MODE=single")
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("config: LOTUS_URL must be an absolute URL like https://lotus.example.com, got %q", c.URL)
		}
	default:
		return fmt.Errorf("config: LOTUS_MODE must be %q or %q, got %q", ModeSaaS, ModeSingle, c.Mode)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS, all subtests.

- [ ] **Step 5: Call Validate at startup**

In `cmd/lotus/main.go`, immediately after `cfg := config.Load()`:

```go
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
```

- [ ] **Step 6: Verify nothing regressed**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS. `AuthHost()` is still called unconditionally by `oauthProviders` — that is fine here because every existing test and deployment is saas mode. Task 2 removes that call.

- [ ] **Step 7: Document the env vars**

Add two rows to `README.md`'s env table:

| `LOTUS_MODE` | `saas` | `saas` = multi-tenant with subdomains; `single` = one organization on one origin (self-hosted / local). |
| `LOTUS_URL` | unset | Absolute base URL of a single-mode install, e.g. `https://lotus.acme.internal`. **Required when `LOTUS_MODE=single`**, unused in `saas` mode. |

Also amend the existing `LOTUS_BASE_DOMAIN` row to note it is required in `saas` mode and ignored in `single` mode.

- [ ] **Step 8: Commit** *(get the user's go-ahead first — see Global Constraints)*

```bash
git add internal/config/config.go internal/config/config_test.go cmd/lotus/main.go README.md
git commit -m "feat(config): add LOTUS_MODE and LOTUS_URL deployment mode switch"
```

---

### Task 2: Generic OIDC provider

A Keycloak-compatible OIDC provider behind the existing `authn.Provider` interface. Independent of every other task — it works in saas mode the moment it lands, and it is the capability that prompted this whole design.

**AC task:** 4.7.

**Files:**
- Create: `internal/authn/provider_oidc.go`
- Create: `internal/authn/provider_oidc_test.go`
- Modify: `cmd/lotus/main.go` (`oauthProviders`)
- Modify: `internal/config/config.go` (OIDC fields)
- Modify: `README.md`

**Interfaces:**
- Consumes: `config.Config.BaseURL()` (Task 1); the existing `authn.Provider` interface and `authn.Identity` struct.
- Produces: `authn.DiscoverOIDC(ctx, issuer, hc) (Endpoints, error)`; `authn.NewOIDCProvider(cfg OIDCConfig) *OIDCProvider`. Registered in the `authn.Registry` under the key `"oidc"`, so its callback path is `/login/oidc/callback`.

- [ ] **Step 1: Write the failing test**

Create `internal/authn/provider_oidc_test.go`:

```go
package authn_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/authn"
)

// newFakeIdP stands in for Keycloak: a discovery document pointing at this
// same server's token and userinfo endpoints. Pointing the provider at an
// httptest.Server rather than a real IdP is the same approach the GitHub
// and Google providers already use.
func newFakeIdP(t *testing.T, userinfo map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/protocol/openid-connect/auth",
			"token_endpoint":         srv.URL + "/protocol/openid-connect/token",
			"userinfo_endpoint":      srv.URL + "/protocol/openid-connect/userinfo",
		})
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// PKCE: the verifier must reach the token endpoint.
		if r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at-" + r.Form.Get("code")})
	})
	mux.HandleFunc("/protocol/openid-connect/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-thecode" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(userinfo)
	})
	return srv
}

func newTestOIDC(t *testing.T, srv *httptest.Server) *authn.OIDCProvider {
	t.Helper()
	eps, err := authn.DiscoverOIDC(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("DiscoverOIDC: %v", err)
	}
	p := authn.NewOIDCProvider(authn.OIDCConfig{
		ClientID:     "lotus",
		ClientSecret: "shh",
		RedirectURL:  "https://lotus.acme.internal/login/oidc/callback",
		DisplayName:  "Keycloak",
		Endpoints:    eps,
	})
	p.HTTPClient = srv.Client()
	return p
}

func TestOIDCProvider_RoundTrip(t *testing.T) {
	srv := newFakeIdP(t, map[string]any{
		"sub": "kc-uuid-1", "email": "you@javan.co.id", "email_verified": true, "name": "Bayu",
	})
	p := newTestOIDC(t, srv)

	if p.Name() != "oidc" {
		t.Fatalf("Name() = %q, want oidc", p.Name())
	}

	authURL := p.AuthCodeURL("thestate", "thechallenge")
	for _, want := range []string{
		"client_id=lotus", "state=thestate", "code_challenge=thechallenge",
		"code_challenge_method=S256", "response_type=code", "scope=openid",
	} {
		if !strings.Contains(authURL, want) {
			t.Errorf("AuthCodeURL() = %q, missing %q", authURL, want)
		}
	}

	token, err := p.Exchange(context.Background(), "thecode", "theverifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	id, err := p.FetchIdentity(context.Background(), token)
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.ProviderSub != "kc-uuid-1" || id.Email != "you@javan.co.id" || !id.EmailVerified || id.Name != "Bayu" {
		t.Fatalf("Identity = %+v, want sub/email/verified/name from userinfo", id)
	}
}

// TestOIDCProvider_UnverifiedEmailIsCarriedThrough matters because
// ResolveUser only links an incoming identity to an existing user when the
// provider reports the email verified. Silently upgrading an IdP's
// email_verified=false to true here would reintroduce the account-takeover
// hole that rule exists to close.
func TestOIDCProvider_UnverifiedEmailIsCarriedThrough(t *testing.T) {
	srv := newFakeIdP(t, map[string]any{
		"sub": "kc-uuid-2", "email": "someone@example.com", "email_verified": false,
	})
	id, err := newTestOIDC(t, srv).FetchIdentity(context.Background(), "at-thecode")
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if id.EmailVerified {
		t.Fatal("EmailVerified = true, want false — the IdP said false")
	}
}

func TestDiscoverOIDC_BadIssuerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	if _, err := authn.DiscoverOIDC(context.Background(), srv.URL, srv.Client()); err == nil {
		t.Fatal("DiscoverOIDC returned nil error for an issuer with no discovery document")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/authn/ -run 'OIDC' -v`
Expected: FAIL — `undefined: authn.DiscoverOIDC`, `undefined: authn.OIDCProvider`.

- [ ] **Step 3: Write the provider**

Create `internal/authn/provider_oidc.go`:

```go
package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OIDCEndpoints are the three endpoints Lotus needs out of an OIDC
// provider's discovery document.
type OIDCEndpoints struct {
	AuthURL     string
	TokenURL    string
	UserInfoURL string
}

// OIDCConfig is the input to NewOIDCProvider.
type OIDCConfig struct {
	ClientID, ClientSecret, RedirectURL string
	// Scopes is space-separated, OAuth-style. Must include "openid".
	Scopes string
	// DisplayName is the login button's label ("Keycloak", "Authentik", …).
	DisplayName string
	Endpoints   OIDCEndpoints
}

// OIDCProvider is a generic OpenID Connect identity provider — Keycloak,
// Authentik, Entra ID, Okta, anything that publishes a discovery document.
// It reads profile claims from the userinfo endpoint rather than verifying
// the ID token against the issuer's JWKS, matching how GoogleProvider
// already works: symmetric with the other providers, and just as easy to
// point at an httptest.Server.
//
// This is deliberately an *instance-wide* provider configured from the
// environment, not a per-organization one. Per-org SSO (its own issuer per
// tenant, domain verification, break-glass access) is a materially
// different, security-critical body of work that stays deferred.
type OIDCProvider struct {
	cfg        OIDCConfig
	HTTPClient *http.Client
}

// DiscoverOIDC fetches <issuer>/.well-known/openid-configuration and reads
// the three endpoints out of it. Preferring discovery over four separately
// configured endpoint URLs removes a whole class of misconfiguration — the
// one where the authorize and token endpoints end up pointing at different
// realms and the failure only shows up mid-login.
func DiscoverOIDC(ctx context.Context, issuer string, hc *http.Client) (OIDCEndpoints, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	docURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return OIDCEndpoints{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return OIDCEndpoints{}, fmt.Errorf("oidc discovery %s: %w", docURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return OIDCEndpoints{}, fmt.Errorf("oidc discovery %s: status %d: %s", docURL, resp.StatusCode, b)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return OIDCEndpoints{}, fmt.Errorf("oidc discovery %s: %w", docURL, err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.UserinfoEndpoint == "" {
		return OIDCEndpoints{}, fmt.Errorf("oidc discovery %s: document is missing one of authorization_endpoint, token_endpoint, userinfo_endpoint", docURL)
	}
	return OIDCEndpoints{
		AuthURL:     doc.AuthorizationEndpoint,
		TokenURL:    doc.TokenEndpoint,
		UserInfoURL: doc.UserinfoEndpoint,
	}, nil
}

func NewOIDCProvider(cfg OIDCConfig) *OIDCProvider {
	if cfg.Scopes == "" {
		cfg.Scopes = "openid profile email"
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "SSO"
	}
	return &OIDCProvider{cfg: cfg, HTTPClient: http.DefaultClient}
}

func (p *OIDCProvider) Name() string { return "oidc" }

// DisplayName is the login button's label. It is not part of the Provider
// interface — the login page type-asserts for it and falls back to the
// provider name, so GitHub and Google need no change.
func (p *OIDCProvider) DisplayName() string { return p.cfg.DisplayName }

func (p *OIDCProvider) AuthCodeURL(state, codeChallenge string) string {
	v := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {p.cfg.Scopes},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return p.cfg.Endpoints.AuthURL + "?" + v.Encode()
}

func (p *OIDCProvider) Exchange(ctx context.Context, code, codeVerifier string) (string, error) {
	form := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {codeVerifier},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("oidc token exchange: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.AccessToken, nil
}

// FetchIdentity reads the standard OIDC claims. email_verified is passed
// through exactly as the IdP reported it — ResolveUser's linking rule
// depends on it being truthful, and an IdP that lets a user self-assert an
// address is precisely the case that rule defends against.
func (p *OIDCProvider) FetchIdentity(ctx context.Context, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoints.UserInfoURL, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return Identity{}, fmt.Errorf("oidc userinfo: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Identity{}, err
	}
	name := body.Name
	if name == "" {
		name = body.PreferredUsername
	}
	return Identity{
		ProviderSub:   body.Sub,
		Email:         body.Email,
		EmailVerified: body.EmailVerified,
		Name:          name,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/authn/ -run 'OIDC' -v`
Expected: PASS — 4 tests.

- [ ] **Step 5: Add OIDC config fields**

In `internal/config/config.go`, add after the Google fields:

```go
	// OIDC (generic OpenID Connect: Keycloak, Authentik, Entra ID, …).
	// Instance-wide, not per-organization. With OIDCClientSecret empty the
	// provider is simply not registered, exactly like GitHub and Google.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCDisplayName  string
	OIDCScopes       string
```

And in `Load()`:

```go
		OIDCIssuer:       os.Getenv("OIDC_ISSUER"),
		OIDCClientID:     os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCDisplayName:  envOr("OIDC_DISPLAY_NAME", "SSO"),
		OIDCScopes:       envOr("OIDC_SCOPES", "openid profile email"),
```

- [ ] **Step 6: Register the provider**

In `cmd/lotus/main.go`, replace `oauthProviders` entirely:

```go
// oauthProviders registers only the providers with a client secret
// configured, so an install works with zero, one, or all three set up.
//
// OIDC discovery runs here, at startup, rather than lazily at first login:
// a wrong OIDC_ISSUER should fail the deploy, where an operator is
// watching, not a user's login, where they can do nothing about it.
func oauthProviders(cfg config.Config) (authn.Registry, error) {
	registry := authn.Registry{}
	base := cfg.BaseURL()
	if cfg.GitHubClientSecret != "" {
		registry["github"] = authn.NewGitHubProvider(cfg.GitHubClientID, cfg.GitHubClientSecret, base+"/login/github/callback")
	}
	if cfg.GoogleClientSecret != "" {
		registry["google"] = authn.NewGoogleProvider(cfg.GoogleClientID, cfg.GoogleClientSecret, base+"/login/google/callback")
	}
	if cfg.OIDCClientSecret != "" {
		if cfg.OIDCIssuer == "" {
			return nil, errors.New("OIDC_CLIENT_SECRET is set but OIDC_ISSUER is empty")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		eps, err := authn.DiscoverOIDC(ctx, cfg.OIDCIssuer, nil)
		if err != nil {
			return nil, err
		}
		registry["oidc"] = authn.NewOIDCProvider(authn.OIDCConfig{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURL:  base + "/login/oidc/callback",
			Scopes:       cfg.OIDCScopes,
			DisplayName:  cfg.OIDCDisplayName,
			Endpoints:    eps,
		})
	}
	return registry, nil
}
```

Update the single call site in `runServe` to handle the new error return, e.g.:

```go
	providers, err := oauthProviders(cfg)
	if err != nil {
		log.Fatalf("oauth providers: %v", err)
	}
```

Add `"context"`, `"errors"`, and `"time"` to that file's imports if not already present.

- [ ] **Step 7: Verify the whole build**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS.

- [ ] **Step 8: Document the env vars**

Add to `README.md`'s env table:

| `OIDC_ISSUER` | unset | OIDC issuer URL, e.g. `https://kc.example.com/realms/lotus`. Endpoints are read from `<issuer>/.well-known/openid-configuration` at startup. Unset means OIDC login is not offered. |
| `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | unset | OIDC client credentials. Redirect URI to register with the IdP: `<LOTUS_URL>/login/oidc/callback` in single mode, `<scheme>://auth.<LOTUS_BASE_DOMAIN>/login/oidc/callback` in saas mode. |
| `OIDC_DISPLAY_NAME` | `SSO` | Label on the login button. |
| `OIDC_SCOPES` | `openid profile email` | Space-separated scopes requested. |

- [ ] **Step 9: Commit** *(get the user's go-ahead first)*

```bash
git add internal/authn/provider_oidc.go internal/authn/provider_oidc_test.go internal/config/config.go cmd/lotus/main.go README.md
git commit -m "feat(authn): add generic OIDC provider with discovery"
```

---

### Task 3: Resolver collapse + route split

The structural heart of the change: one origin satisfying both host guards, and SaaS-only routes disappearing from the route table in single mode.

**AC task:** 4.8.

**Files:**
- Modify: `internal/tenant/resolve.go`
- Modify: `internal/tenant/resolve_test.go`
- Create: `internal/repo/global/bootstrap.go` (the single-org query lands here; Task 4 adds the rest)
- Modify: `internal/web/server.go`
- Modify: `internal/web/route_coverage_test.go`

**Interfaces:**
- Consumes: `config.Config.IsSingle()` (Task 1).
- Produces: `tenant.Resolved.Single bool`; `global.TheOnlyOrg(ctx, db) (*Org, error)` and `global.ErrNoOrg`. Task 4 reuses `TheOnlyOrg`.

- [ ] **Step 1: Write the failing resolver test**

Append to `internal/tenant/resolve_test.go`:

```go
// newSingleModeApp builds the same two guarded routes as newTestApp, but
// with a resolver in single mode — where both guards must pass on one
// origin, whatever Host the request carries.
func newSingleModeApp(t *testing.T) (*fiber.App, *sqlx.DB) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{Mode: config.ModeSingle, URL: "http://lotus.acme.internal", Dev: true}
	resolver := tenant.NewResolver(cfg, db)

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/tenant-only", tenant.RequireTenant(), func(c *fiber.Ctx) error {
		org, _ := tenant.OrgFromContext(c)
		return c.SendString("tenant:" + org.Subdomain)
	})
	app.Get("/auth-only", tenant.RequireAuth(), func(c *fiber.Ctx) error {
		return c.SendString("auth-ok")
	})
	return app, db
}

// TestSingleMode_OneOriginSatisfiesBothGuards is the core acceptance
// criterion of the single-tenant design: RequireAuth and RequireTenant are
// mutually exclusive in saas mode, and must both pass here, on the same
// origin, for any Host at all — a self-hoster may reach the app by
// hostname, by bare IP, or through a proxy that rewrites Host entirely.
func TestSingleMode_OneOriginSatisfiesBothGuards(t *testing.T) {
	app, db := newSingleModeApp(t)
	mustInsertTestOrg(t, db, "javan", nil)

	for _, host := range []string{"lotus.acme.internal", "192.0.2.10:8080", "anything.at.all"} {
		for _, path := range []string{"/tenant-only", "/auth-only"} {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
			req.Host = host
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("%s %s: %v", host, path, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s: status = %d, want 200", host, path, resp.StatusCode)
			}
		}
	}
}

// TestSingleMode_IgnoresDevOrgHeader: X-Lotus-Org selects between tenants,
// and in single mode there is nothing to select between. Honoring it would
// be a way to ask for an organization that cannot exist.
func TestSingleMode_IgnoresDevOrgHeader(t *testing.T) {
	app, db := newSingleModeApp(t)
	mustInsertTestOrg(t, db, "javan", nil)

	req := httptest.NewRequest(http.MethodGet, "http://lotus.acme.internal/tenant-only", nil)
	req.Host = "lotus.acme.internal"
	req.Header.Set(tenant.DevOrgHeader, "someoneelse")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the header must be ignored, not honored or rejected)", resp.StatusCode)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if got := string(body[:n]); got != "tenant:javan" {
		t.Fatalf("body = %q, want tenant:javan", got)
	}
}

// TestSingleMode_NoOrgYetIs404 covers the window between `lotus migrate`
// and `lotus init`: the database is up but no organization exists. Every
// request must 404 rather than panic on a nil org.
func TestSingleMode_NoOrgYetIs404(t *testing.T) {
	app, _ := newSingleModeApp(t)

	req := httptest.NewRequest(http.MethodGet, "http://lotus.acme.internal/tenant-only", nil)
	req.Host = "lotus.acme.internal"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 before lotus init has run", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tenant/ -run SingleMode -v`
Expected: FAIL — `config.ModeSingle` resolves (Task 1 landed) but every request 404s, because `classify` finds no base-domain suffix match.

- [ ] **Step 3: Add the single-org query**

Create `internal/repo/global/bootstrap.go`:

```go
package global

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// ErrNoOrg means the database has no live organization. In single mode
// that is the state between `lotus migrate` and `lotus init`, not an
// error condition worth a 500 — the resolver turns it into a 404.
var ErrNoOrg = errors.New("global: no organization exists")

// TheOnlyOrg returns the single organization a single-mode install holds.
// LIMIT 2 rather than LIMIT 1 on purpose: if a second organization ever
// appears in a single-mode database, that is a real invariant violation
// and must be loud, not silently resolved to whichever row sorted first.
func TheOnlyOrg(ctx context.Context, db *sqlx.DB) (*Org, error) {
	var orgs []Org
	err := db.SelectContext(ctx, &orgs,
		`SELECT id, subdomain, name, perm_epoch, deleted_at
		   FROM organizations
		  WHERE deleted_at IS NULL
		  ORDER BY created_at
		  LIMIT 2`)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	switch len(orgs) {
	case 0:
		return nil, ErrNoOrg
	case 1:
		return &orgs[0], nil
	default:
		return nil, errors.New("global: single mode requires exactly one organization, found more than one")
	}
}
```

- [ ] **Step 4: Add the single-mode branch to the resolver**

In `internal/tenant/resolve.go`, add a field to `Resolved`:

```go
	// Single is true when this resolution came from a single-mode install,
	// where one origin serves what saas mode splits across the auth host
	// and a tenant host. The guards below read it so a route registered
	// for either host works on that one origin.
	Single bool
```

Add a package-level constant next to `DevOrgHeader`:

```go
// singleOrgCacheKey is the orgCache key single mode caches its one
// organization under. It is not a valid subdomain label (labels are
// alphanumeric-and-hyphen, checked by ValidSubdomainLabel), so it can
// never collide with a saas-mode entry.
const singleOrgCacheKey = "\x00single"
```

Change `classify`'s opening:

```go
func (r *Resolver) classify(ctx context.Context, host, devOrgHeader string) (Resolved, error) {
	if r.cfg.IsSingle() {
		// No host classification at all: there is no base domain to match
		// against, and a self-hoster may reach the app by hostname, by bare
		// IP, or through a proxy that rewrites Host. The dev org header is
		// ignored for the same reason — it selects between tenants, and
		// there is only one.
		return r.classifySingle(ctx)
	}

	if devOrgHeader != "" {
		return r.classifyTenantLabel(ctx, strings.ToLower(devOrgHeader))
	}
	// ... rest unchanged
```

Add the new method below `classifyTenantLabel`:

```go
func (r *Resolver) classifySingle(ctx context.Context) (Resolved, error) {
	if org, ok := r.cache.get(singleOrgCacheKey); ok {
		return singleResolved(org), nil
	}
	org, err := global.TheOnlyOrg(ctx, r.db)
	if err != nil {
		if errors.Is(err, global.ErrNoOrg) {
			// Between `lotus migrate` and `lotus init`. 404 like any other
			// unresolvable host rather than 500 — nothing is broken, the
			// install simply is not initialized yet.
			return Resolved{Kind: HostUnknown, Single: true}, nil
		}
		return Resolved{}, err
	}
	r.cache.set(singleOrgCacheKey, org)
	return singleResolved(org), nil
}

func singleResolved(org *global.Org) Resolved {
	return Resolved{Kind: HostTenant, Org: org, Single: true}
}
```

Finally make `RequireAuth` mode-aware (`RequireTenant` needs no change — single mode already yields `HostTenant`):

```go
// RequireAuth 404s any request whose Host didn't resolve to the auth host,
// so a route registered for auth.lotus.com is unreachable from any tenant
// subdomain (and vice versa via RequireTenant) — closing a bug class that's
// easy to miss in review.
//
// In single mode that separation does not exist: one origin serves both,
// so a resolution flagged Single passes here as well as through
// RequireTenant. Which guard applies therefore depends on the mode, which
// is exactly why TestRouteCoverage runs against both.
func RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		resolved, ok := c.Locals(localsResolvedKey).(Resolved)
		if !ok {
			return fiber.ErrNotFound
		}
		if resolved.Single {
			return nextIfResolved(c, resolved)
		}
		if resolved.Kind != HostAuth {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}

// nextIfResolved lets a single-mode request through only once an
// organization actually exists — before `lotus init`, Kind is HostUnknown
// and there is nothing to serve.
func nextIfResolved(c *fiber.Ctx, resolved Resolved) error {
	if resolved.Kind != HostTenant {
		return fiber.ErrNotFound
	}
	return c.Next()
}
```

Add `"errors"` and the `global` import if the file does not already have them (it imports both).

- [ ] **Step 5: Run resolver tests**

Run: `go test ./internal/tenant/ -v -count=1`
Expected: PASS — the three new single-mode tests plus every existing saas test.

- [ ] **Step 6: Write the failing route-coverage test**

In `internal/web/route_coverage_test.go`, replace `buildFullAppForCoverage` and `TestRouteCoverage`'s first line:

```go
func buildFullAppForCoverage(t *testing.T, mode string) *fiber.App {
	t.Helper()
	db := dbtest.Open(t)
	renderer := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	cfg := config.Config{Mode: mode, BaseDomain: "lotus.test", URL: "http://lotus.test", Dev: true}
	resolver := tenant.NewResolver(cfg, db)
	return New(Deps{
		Renderer:       renderer,
		StaticFS:       os.DirFS("../../web/static"),
		Resolver:       resolver,
		DB:             db,
		Cfg:            cfg,
		OAuthProviders: authn.Registry{},
	})
}
```

and wrap the body of `TestRouteCoverage` in a mode loop:

```go
func TestRouteCoverage(t *testing.T) {
	// Both modes: single mode registers a different route set, and a route
	// correctly guarded in one mode could be bare in the other. Running the
	// scan twice is what makes that a CI failure rather than a review
	// burden — see the design doc's §4.2.
	for _, mode := range []string{config.ModeSaaS, config.ModeSingle} {
		t.Run(mode, func(t *testing.T) {
			app := buildFullAppForCoverage(t, mode)
			violations, publicRoutes := scanRouteCoverage(app)
			// ... the existing /static check and violation reporting, unchanged
		})
	}
}
```

Keep the existing `/static` block and the final `t.Fatalf` inside the subtest body verbatim.

- [ ] **Step 7: Run it to verify it fails**

Run: `go test ./internal/web/ -run TestRouteCoverage -v`
Expected: FAIL in the `single` subtest — every SaaS-only route is still registered, and the apex `/` still renders the Fase-0 placeholder.

- [ ] **Step 8: Split the route table**

In `internal/web/server.go`, replace the apex route (line 75) and extract the SaaS-only block. Replace line 75 with:

```go
	if d.Cfg.IsSingle() {
		// One origin means there is no separate marketing surface to land
		// on: / is the app. Redirecting rather than rendering the dashboard
		// here keeps a single canonical URL for it.
		app.Get("/", authz.Public("redirect only; the dashboard behind it enforces its own session"),
			func(c *fiber.Ctx) error { return c.Redirect("/dashboard", fiber.StatusFound) })
	} else {
		app.Get("/", authz.Public("apex placeholder; a real marketing page is not yet scoped"), home(d.Renderer))
	}
```

Then move the handoff, signup/onboarding, and invite-flow registrations (current lines 94–141) into a new function at the bottom of the file, and call it conditionally where they used to be:

```go
	// The auth host, the handoff, and self-serve onboarding all exist only
	// because saas mode splits one product across three hosts and lets
	// anyone create an organization. A single-mode install has one origin
	// and one organization created by `lotus init`, so these routes are not
	// registered at all — the route table itself is the documentation of
	// what exists where.
	if !d.Cfg.IsSingle() {
		registerSaaSAuthRoutes(app, authDeps)
	} else {
		registerSingleModeAuthRoutes(app, authDeps)
	}
```

```go
// registerSaaSAuthRoutes registers everything that only makes sense when
// the auth host and tenant hosts are different origins.
func registerSaaSAuthRoutes(app *fiber.App, authDeps authn.Deps) {
	// ... the existing /login/:provider, /login/:provider/callback,
	// /handoff/authorize, /handoff/start, /handoff/finish, /signup,
	// /welcome, /onboarding/*, and /invite/* registrations, moved
	// verbatim including their comments.
}

// registerSingleModeAuthRoutes registers the subset that still applies on
// one origin: OAuth/OIDC login itself, and the invitation flow that is how
// members join an install whose organization already exists.
func registerSingleModeAuthRoutes(app *fiber.App, authDeps authn.Deps) {
	app.Get("/login/:provider", tenant.RequireAuth(),
		authz.Public("starts OAuth; there is no session yet by definition"), authn.Login(authDeps))
	app.Get("/login/:provider/callback", tenant.RequireAuth(),
		authz.Public("OAuth provider redirect target; establishes the session"), authn.Callback(authDeps))

	app.Get("/invite/:token", tenant.RequireAuth(),
		authz.Public("pre-session by definition; shows the accept/login prompt"), authn.InviteLanding(authDeps))
	app.Post("/invite/:token/confirm", tenant.RequireAuth(),
		authz.Public("explicit user-confirmed accept for a mismatched-email invite"), authn.InviteConfirm(authDeps))
	app.Post("/invite/:token/decline", tenant.RequireAuth(),
		authz.Public("declining must not require a login the person never wanted"), authn.InviteDecline(authDeps))
}
```

- [ ] **Step 9: Run the full suite**

Run: `go test ./... -count=1`
Expected: PASS, including both `TestRouteCoverage` subtests.

- [ ] **Step 10: Commit** *(get the user's go-ahead first)*

```bash
git add internal/tenant/ internal/repo/global/bootstrap.go internal/web/server.go internal/web/route_coverage_test.go
git commit -m "feat(tenant): collapse host taxonomy in single mode, split route table"
```

---

### Task 4: `lotus init` and the ownership claim

Creates the organization ahead of first boot and hands ownership to the first verified matching login. This is what makes a fresh instance usable without a land-grab window.

**AC task:** 4.9.

**Files:**
- Create: `internal/db/migrations/00021_instance_bootstrap.sql`
- Modify: `internal/repo/global/bootstrap.go`
- Create: `internal/authn/bootstrap.go`
- Create: `internal/authn/bootstrap_test.go`
- Create: `cmd/lotus/init.go`
- Modify: `cmd/lotus/main.go`

**Interfaces:**
- Consumes: `global.TheOnlyOrg` / `global.ErrNoOrg` (Task 3); the existing `global.CreateOrgWithOwner(ctx, db, subdomain, name, ownerUserID) (uuid.UUID, error)`; the existing `authn.ResolveUser` identity path.
- Produces: `global.CreateBootstrap(ctx, db, orgID uuid.UUID, ownerEmail string) error`; `global.PendingBootstrap(ctx, db) (*Bootstrap, error)`; `global.ConsumeBootstrap(ctx, db, userID uuid.UUID) error`; `authn.ClaimOwnershipIfPending(ctx, db, userID uuid.UUID, id Identity) (claimed bool, err error)`.

- [ ] **Step 1: Write the migration**

Create `internal/db/migrations/00021_instance_bootstrap.sql`:

```sql
-- Single-mode bootstrap (design doc 2026-08-12, §4.5).
--
-- `lotus init` creates the organization ahead of first boot, but cannot
-- create its Owner: memberships.user_id is NOT NULL and nobody has logged
-- in yet. So the org is inserted with zero memberships — legal, because
-- enforce_min_one_owner fires BEFORE UPDATE OR DELETE, never on INSERT —
-- and the pending owner's email is parked here until the first login with
-- a matching, verified address claims it.
--
-- The boolean primary key with a CHECK is the "at most one row" idiom: a
-- second `lotus init` fails on the primary key even if the application's
-- own check is somehow bypassed.
CREATE TABLE instance_bootstrap (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    owner_email text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz,
    consumed_by uuid REFERENCES users (id)
);
```

- [ ] **Step 2: Run the migration**

Run: `make migrate-up`
Expected: applies `00021` with no error.

- [ ] **Step 3: Write the failing test**

Create `internal/authn/bootstrap_test.go`:

```go
package authn_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

// seedBootstrap mirrors what `lotus init` does: an organization with zero
// memberships plus a pending bootstrap row.
func seedBootstrap(t *testing.T, ownerEmail string) (*global.Org, func(email string, verified bool) (uuid.UUID, error)) {
	t.Helper()
	db := dbtest.Open(t)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := db.GetContext(ctx, &orgID,
		`INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := global.CreateBootstrap(ctx, db, orgID, ownerEmail); err != nil {
		t.Fatalf("CreateBootstrap: %v", err)
	}
	org, err := global.TheOnlyOrg(ctx, db)
	if err != nil {
		t.Fatalf("TheOnlyOrg: %v", err)
	}

	claim := func(email string, verified bool) (uuid.UUID, error) {
		var userID uuid.UUID
		if err := db.GetContext(ctx, &userID,
			`INSERT INTO users (email) VALUES ($1) RETURNING id`, email); err != nil {
			return uuid.Nil, err
		}
		_, err := authn.ClaimOwnershipIfPending(ctx, db, userID,
			authn.Identity{Email: email, EmailVerified: verified})
		return userID, err
	}
	return org, claim
}

func ownerCount(t *testing.T, orgID uuid.UUID) int {
	t.Helper()
	db := dbtest.Open(t)
	var n int
	if err := db.Get(&n,
		`SELECT count(*) FROM memberships m
		   JOIN roles r ON r.id = m.role_id
		  WHERE m.org_id = $1 AND r.key = 'owner' AND m.status = 'active'`, orgID); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	return n
}

func TestClaimOwnership_MatchingVerifiedEmailBecomesOwner(t *testing.T) {
	org, claim := seedBootstrap(t, "you@javan.co.id")
	if _, err := claim("you@javan.co.id", true); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if n := ownerCount(t, org.ID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}

// TestClaimOwnership_UnverifiedEmailIsRejected is the security case: an
// IdP that lets a user self-assert an arbitrary address must not be able
// to hand them ownership of a fresh instance.
func TestClaimOwnership_UnverifiedEmailIsRejected(t *testing.T) {
	org, claim := seedBootstrap(t, "you@javan.co.id")
	if _, err := claim("you@javan.co.id", false); err == nil {
		t.Fatal("claim with an unverified email returned nil error, want rejection")
	}
	if n := ownerCount(t, org.ID); n != 0 {
		t.Fatalf("owner count = %d, want 0", n)
	}
}

func TestClaimOwnership_NonMatchingEmailIsRejected(t *testing.T) {
	org, claim := seedBootstrap(t, "you@javan.co.id")
	if _, err := claim("someoneelse@example.com", true); err == nil {
		t.Fatal("claim with a non-matching email returned nil error, want rejection")
	}
	if n := ownerCount(t, org.ID); n != 0 {
		t.Fatalf("owner count = %d, want 0", n)
	}
}

// TestClaimOwnership_MatchIsCaseInsensitive: email addresses are not
// case-sensitive in practice and an operator typing `--owner-email` at a
// shell should not have to match their IdP's casing exactly.
func TestClaimOwnership_MatchIsCaseInsensitive(t *testing.T) {
	org, claim := seedBootstrap(t, "You@Javan.co.id")
	if _, err := claim("you@javan.co.id", true); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if n := ownerCount(t, org.ID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}

// TestClaimOwnership_ConsumedBootstrapIsInert: once ownership is claimed,
// a second login with the same address must not add a second Owner
// membership or otherwise re-run the claim.
func TestClaimOwnership_ConsumedBootstrapIsInert(t *testing.T) {
	org, claim := seedBootstrap(t, "you@javan.co.id")
	if _, err := claim("you@javan.co.id", true); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	claimed, err := func() (bool, error) {
		db := dbtest.Open(t)
		var userID uuid.UUID
		if err := db.Get(&userID, `INSERT INTO users (email) VALUES ('later@javan.co.id') RETURNING id`); err != nil {
			return false, err
		}
		return authn.ClaimOwnershipIfPending(context.Background(), db, userID,
			authn.Identity{Email: "you@javan.co.id", EmailVerified: true})
	}()
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("claimed = true on an already-consumed bootstrap, want false")
	}
	if n := ownerCount(t, org.ID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/authn/ -run ClaimOwnership -v`
Expected: FAIL — `undefined: global.CreateBootstrap`, `undefined: authn.ClaimOwnershipIfPending`.

- [ ] **Step 5: Add the repo functions**

Append to `internal/repo/global/bootstrap.go`:

```go
// Bootstrap is a pending single-mode ownership claim, written by
// `lotus init` and consumed by the first matching verified login.
type Bootstrap struct {
	OrgID      uuid.UUID  `db:"org_id"`
	OwnerEmail string     `db:"owner_email"`
	ConsumedAt *time.Time `db:"consumed_at"`
}

// ErrBootstrapExists means `lotus init` has already run against this
// database. A single-mode install holds exactly one organization.
var ErrBootstrapExists = errors.New("global: this instance is already initialized")

func CreateBootstrap(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, ownerEmail string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO instance_bootstrap (org_id, owner_email) VALUES ($1, $2)`, orgID, ownerEmail)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrBootstrapExists
	}
	return err
}

// PendingBootstrap returns the unconsumed bootstrap row, or nil when there
// is none — either because `lotus init` never ran or because ownership has
// already been claimed.
func PendingBootstrap(ctx context.Context, db *sqlx.DB) (*Bootstrap, error) {
	var b Bootstrap
	err := db.GetContext(ctx, &b,
		`SELECT org_id, owner_email, consumed_at FROM instance_bootstrap WHERE consumed_at IS NULL`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ConsumeBootstrap inserts the Owner membership and marks the bootstrap
// consumed in one transaction, so a crash between the two cannot leave an
// instance that is both owner-less and no longer claimable.
func ConsumeBootstrap(ctx context.Context, db *sqlx.DB, userID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var b Bootstrap
	// FOR UPDATE serializes two simultaneous first-logins: the loser sees
	// no unconsumed row and claims nothing.
	err = tx.GetContext(ctx, &b,
		`SELECT org_id, owner_email, consumed_at FROM instance_bootstrap
		  WHERE consumed_at IS NULL FOR UPDATE`)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBootstrapExists
	}
	if err != nil {
		return err
	}

	var ownerRoleID uuid.UUID
	if err := tx.GetContext(ctx, &ownerRoleID,
		`SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`,
		b.OrgID, userID, ownerRoleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE instance_bootstrap SET consumed_at = now(), consumed_by = $1 WHERE consumed_at IS NULL`,
		userID); err != nil {
		return err
	}
	return tx.Commit()
}
```

Add `"time"`, `"github.com/google/uuid"`, and `"github.com/jackc/pgx/v5/pgconn"` to that file's imports.

- [ ] **Step 6: Add the claim helper**

Create `internal/authn/bootstrap.go`:

```go
package authn

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"
)

// ErrNotTheOwner is returned when someone logs into a not-yet-claimed
// single-mode instance with an address that is not the one `lotus init`
// nominated. Until an Owner exists, nobody else gets in — otherwise a
// fresh instance reachable from the internet is a land-grab.
var ErrNotTheOwner = errors.New("authn: this instance is awaiting its first Owner login")

// ClaimOwnershipIfPending is the single-mode first-login step. It reports
// whether this login claimed ownership; false with a nil error means there
// was nothing pending (the normal steady state), so callers can invoke it
// unconditionally on every login.
//
// The verified-email requirement is the load-bearing part: without it, an
// IdP that lets a user self-assert an arbitrary address would hand
// ownership of a fresh instance to whoever asked first.
func ClaimOwnershipIfPending(ctx context.Context, db *sqlx.DB, userID uuid.UUID, id Identity) (bool, error) {
	pending, err := global.PendingBootstrap(ctx, db)
	if err != nil || pending == nil {
		return false, err
	}
	if !strings.EqualFold(strings.TrimSpace(id.Email), strings.TrimSpace(pending.OwnerEmail)) {
		return false, ErrNotTheOwner
	}
	if !id.EmailVerified {
		return false, ErrNotTheOwner
	}
	if err := global.ConsumeBootstrap(ctx, db, userID); err != nil {
		if errors.Is(err, global.ErrBootstrapExists) {
			return false, nil // lost the race; someone else claimed it
		}
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/authn/ -run ClaimOwnership -v`
Expected: PASS — 5 tests.

- [ ] **Step 8: Wire the claim into the login callback**

In `internal/authn/handlers.go`'s `Callback` handler, after `ResolveUser` has produced a user and before the session is issued, add — guarded so saas mode is untouched:

```go
	if deps.Cfg.IsSingle() {
		if _, err := ClaimOwnershipIfPending(c.Context(), deps.DB, user.ID, identity); err != nil {
			if errors.Is(err, ErrNotTheOwner) {
				return fiber.NewError(fiber.StatusForbidden,
					"This instance is waiting for its first Owner to sign in.")
			}
			return err
		}
	}
```

Match the surrounding names for `user` and `identity` — read the handler first rather than assuming.

- [ ] **Step 9: Write the `init` subcommand**

Create `cmd/lotus/init.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/repo/global"
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9-]+`)

// runInit is the single-mode bootstrap command: it creates the one
// organization and parks the pending Owner's email, so the first verified
// matching login becomes Owner. Run once, before the first `serve`.
func runInit(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	orgName := fs.String("org-name", "", "organization display name (required)")
	ownerEmail := fs.String("owner-email", "", "email address that will become Owner on first login (required)")
	slug := fs.String("slug", "", "organization slug; defaults to a slugified --org-name")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *orgName == "" || *ownerEmail == "" {
		fs.Usage()
		log.Fatal("lotus init: --org-name and --owner-email are both required")
	}
	if *slug == "" {
		*slug = slugify(*orgName)
	}
	if *slug == "" {
		log.Fatalf("lotus init: could not derive a slug from --org-name %q; pass --slug explicitly", *orgName)
	}

	conn := db.MustOpen(cfg.DatabaseURL)
	defer conn.Close()
	ctx := context.Background()

	// An organization already present means this instance is initialized.
	// Checking before inserting gives a clear message; instance_bootstrap's
	// single-row primary key is the backstop if two inits race.
	if _, err := global.TheOnlyOrg(ctx, conn); err == nil {
		log.Fatal("lotus init: this instance already has an organization; a single-mode install holds exactly one")
	} else if !errors.Is(err, global.ErrNoOrg) {
		log.Fatal(err)
	}

	var orgID uuid.UUID
	if err := conn.GetContext(ctx, &orgID,
		`INSERT INTO organizations (subdomain, name) VALUES ($1, $2) RETURNING id`, *slug, *orgName); err != nil {
		log.Fatalf("lotus init: create organization: %v", err)
	}
	if err := global.CreateBootstrap(ctx, conn, orgID, *ownerEmail); err != nil {
		log.Fatalf("lotus init: %v", err)
	}

	fmt.Printf("Initialized %q (slug %q).\n", *orgName, *slug)
	fmt.Printf("Sign in as %s to claim ownership. No other account can sign in until then.\n", *ownerEmail)
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = nonSlugChars.ReplaceAllString(s, "")
	return strings.Trim(s, "-")
}
```

Check `internal/db`'s actual open helper name before using `db.MustOpen` — match whatever `runMigrate` and `runServe` already call.

- [ ] **Step 10: Register the subcommand**

In `cmd/lotus/main.go`'s switch, add before `default:`:

```go
	case "init":
		runInit(cfg, os.Args[2:])
```

and extend `usage()`:

```go
	fmt.Fprintln(os.Stderr, "usage: lotus <serve|migrate up|migrate down|init --org-name=<name> --owner-email=<email> [--slug=<slug>]|rewrap-keys --old-kek=<base64>|import-legacy>")
```

- [ ] **Step 11: Verify end to end by hand**

```bash
LOTUS_MODE=single LOTUS_URL=http://localhost:8080 go run ./cmd/lotus init --org-name="Javan" --owner-email=you@javan.co.id
```

Expected: prints the two confirmation lines. Running it a second time must fail with "already has an organization".

- [ ] **Step 12: Run the full suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 13: Commit** *(get the user's go-ahead first)*

```bash
git add internal/db/migrations/00021_instance_bootstrap.sql internal/repo/global/bootstrap.go internal/authn/bootstrap.go internal/authn/bootstrap_test.go internal/authn/handlers.go cmd/lotus/init.go cmd/lotus/main.go
git commit -m "feat(cmd): add lotus init and first-login ownership claim"
```

---

### Task 5: Single-mode e2e, Keycloak compose, and docs

Proves the whole thing works against a real Keycloak, offline, in CI.

**AC task:** 4.8 (e2e) and a 5.2 amendment comment.

**Files:**
- Create: `docker-compose.yml`
- Create: `deploy/keycloak/realm-lotus.json`
- Create: `e2e/tests/single-mode.spec.ts`
- Modify: `e2e/playwright.config.ts`
- Modify: `README.md`
- Modify: `Makefile`

**Interfaces:**
- Consumes: everything from Tasks 1–4.
- Produces: a `single` Playwright project; a `make dev-single` target.

- [ ] **Step 1: Write the compose file**

Create `docker-compose.yml`:

```yaml
# Local/CI dependencies. Postgres mirrors what `make db-up` already starts;
# Keycloak exists so the OIDC path can be exercised offline, against a real
# IdP, instead of only against the httptest fake in the Go tests.
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_HOST_AUTH_METHOD: trust
      POSTGRES_DB: lotus
    ports: ["5418:5432"]

  keycloak:
    image: quay.io/keycloak/keycloak:26.0
    command: ["start-dev", "--import-realm"]
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: admin
      KC_HTTP_PORT: "8081"
    volumes:
      - ./deploy/keycloak:/opt/keycloak/data/import:ro
    ports: ["8081:8081"]
```

- [ ] **Step 2: Write the realm import**

Create `deploy/keycloak/realm-lotus.json` with a realm named `lotus`, a confidential client `lotus` whose `redirectUris` includes `http://localhost:8096/login/oidc/callback`, and one user with a **verified** email matching the address the e2e test passes to `lotus init`. Generate it rather than hand-writing: start Keycloak, configure it through the admin console, then export with

```bash
docker compose exec keycloak /opt/keycloak/bin/kc.sh export --dir /tmp/export --realm lotus
```

and copy the result in. Set the client secret to a fixed literal (this file is a test fixture, never a production credential — say so in a comment at the top of the README section that references it).

- [ ] **Step 3: Write the failing e2e test**

Create `e2e/tests/single-mode.spec.ts`:

```ts
import { expect, test } from '@playwright/test';

const ORIGIN = process.env.LOTUS_SINGLE_ORIGIN ?? 'http://localhost:8096';

// Single mode collapses the three hosts into one origin, so the whole
// point is that no subdomain appears anywhere in these URLs.
test('apex redirects to the dashboard instead of a marketing placeholder', async ({ page }) => {
  const response = await page.goto(`${ORIGIN}/`);
  expect(response?.status()).toBeLessThan(400);
  // Not signed in yet, so the dashboard's own session guard takes over —
  // what matters here is that / is no longer the Fase-0 scaffold page.
  await expect(page.getByText('Scaffold is up')).toHaveCount(0);
});

test('saas-only routes are not registered', async ({ page }) => {
  for (const path of ['/signup', '/onboarding/org', '/handoff/start']) {
    const response = await page.goto(`${ORIGIN}${path}`);
    expect(response?.status(), `${path} should not exist in single mode`).toBe(404);
  }
});

test('login through Keycloak claims ownership and lands on the dashboard', async ({ page }) => {
  await page.goto(`${ORIGIN}/login/oidc`);
  await page.getByLabel(/username|email/i).fill('you@javan.co.id');
  await page.getByLabel(/password/i).fill('lotus-e2e');
  await page.getByRole('button', { name: /sign in|log in/i }).click();

  await expect(page).toHaveURL(new RegExp(`^${ORIGIN}/dashboard`));
  await expect(page.getByText('Javan')).toBeVisible();
});
```

- [ ] **Step 4: Add the Playwright project**

In `e2e/playwright.config.ts`, add a `single` project alongside the existing one, with its own `webServer` entry that starts the app with `LOTUS_MODE=single`, `LOTUS_URL=http://localhost:8096`, `PORT`/`ADDR=:8096`, and the `OIDC_*` vars pointing at `http://localhost:8081/realms/lotus`. Follow the existing config's shape exactly; do not restructure it.

- [ ] **Step 5: Run the e2e suite**

```bash
docker compose up -d
make migrate-up
LOTUS_MODE=single LOTUS_URL=http://localhost:8096 go run ./cmd/lotus init --org-name="Javan" --owner-email=you@javan.co.id
npx playwright test --project=single
```

Expected: 3 tests pass.

- [ ] **Step 6: Add a Makefile target**

```make
dev-single: $(TAILWIND) db-up
	LOTUS_DEV=1 LOTUS_MODE=single LOTUS_URL=http://localhost:8080 air
```

- [ ] **Step 7: Document the self-hosted install**

Add a `## Self-hosted install` section to `README.md`:

````markdown
Lotus installs three ways off one binary: multi-tenant SaaS (`LOTUS_MODE=saas`,
the default), and self-hosted or local single-organization (`LOTUS_MODE=single`).
A single-mode install serves one origin and holds exactly one organization, so it
needs no wildcard DNS and no wildcard certificate — an ordinary single-name cert
is enough.

```bash
export LOTUS_MODE=single
export LOTUS_URL=https://lotus.acme.internal
export DATABASE_URL=postgres://...
export LOTUS_KEK=$(openssl rand -base64 32)

lotus migrate up
lotus init --org-name="Acme" --owner-email=admin@acme.com
lotus serve
```

Sign in as the `--owner-email` address to claim ownership. No other account can
sign in until that happens. Everyone else joins by invitation — there is no
self-serve signup in single mode.

For SSO, point `OIDC_ISSUER` at your Keycloak realm and register
`https://lotus.acme.internal/login/oidc/callback` as the client's redirect URI.
````

- [ ] **Step 8: Commit** *(get the user's go-ahead first)*

```bash
git add docker-compose.yml deploy/keycloak/ e2e/ Makefile README.md
git commit -m "test(e2e): single-mode suite with Keycloak, plus self-hosted docs"
```

---

### Task 6: ActiveCollab accountability pass

The audit trail. Do this **after** each implementation task, not as a batch at the end.

**Files:** none (ActiveCollab only).

- [ ] **Step 1: Create the four new tasks** in project 697, if they do not already exist. Use `mcp__collab__create_task` with `project_id: 697` and `task_list_id: 4384` (Fase 4) for 4.7/4.8/4.9, `4385` (Fase 5) for the 5.2 amendment:
  - `4.7 Generic OIDC provider (env-var configured)` — body: design doc §4.4, scope, acceptance criteria.
  - `4.8 Single-tenant mode: resolver collapse + route split` — body: design doc §4.1–§4.3, §8.
  - `4.9 lotus init bootstrap + instance_bootstrap migration` — body: design doc §4.5, §5.
  - `5.2 amendment: second deploy path for single-name TLS` — body: design doc §9.2.

- [ ] **Step 2: Label them** with `mcp__collab__set_task_labels`, `labels: ["BACKLOG"]`. **Labels are real ActiveCollab labels, never title prefixes** — a previous agent wrote `[TENSION]` and `[READY FOR TEST]` into task names, which is the mistake this project just finished cleaning up.

- [ ] **Step 3: On completing each implementation task**, post an implementation-report comment on its AC task with `mcp__collab__add_comment` covering: what shipped, files touched, tests added and their results, and anything deferred. Then set that task's label to `READY FOR TEST`.

- [ ] **Step 4: Log new tensions as they surface**, as their own task with the `TENSION` label, filed in the relevant phase list — matching the convention already established across the project's 79 tasks.

---

## Self-Review

**Spec coverage** — every design-doc section maps to a task: §4.1 → Task 1; §4.2 → Task 3 steps 3–5; §4.3 → Task 3 step 8; §4.4 → Task 2; §4.5 → Task 4; §5 → Task 4 step 1; §6 → Tasks 1, 2, 5 (README); §7 (no change to Fase 2–4) → verified by `go test ./...` in Tasks 1–4; §8 → Task 3 steps 6–7 and Task 5; §9.2 (5.2 amendment) → Task 6; §10 → Task 6.

**Deliberately deferred, with the reason:**
- §9.4 (SaaS apex marketing page) — predates this design; no task covers it and none is added here.
- §9.5 (single → SaaS migration path) — declared a non-goal in §3.

**Type consistency** — `Config.IsSingle()` / `Config.BaseURL()` are defined in Task 1 and used in Tasks 2, 3, 4, 5. `Resolved.Single` is defined in Task 3 step 4 and read in the same step's guards. `global.TheOnlyOrg` / `global.ErrNoOrg` are defined in Task 3 step 3 and reused in Task 4 step 9. `authn.OIDCConfig` / `authn.OIDCEndpoints` / `authn.DiscoverOIDC` are defined in Task 2 step 3 and consumed in step 6. `global.CreateBootstrap` / `PendingBootstrap` / `ConsumeBootstrap` are defined in Task 4 step 5 and consumed in step 6 and step 9.

**Two steps deliberately describe rather than show code**, because inventing it would be worse than pointing at it:
- Task 4 step 8 (wiring the claim into `Callback`) — the surrounding variable names must be read from `handlers.go`, not guessed.
- Task 5 steps 2 and 4 (Keycloak realm export, Playwright project) — both must match generated/existing file shapes exactly.

Both say so explicitly and name what to read first.
