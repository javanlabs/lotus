// Package config loads Lotus's runtime configuration from the environment.
package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// devOnlyKEK is a fixed, public, 32-byte AES-256 key used only when
// LOTUS_KEK is unset (local dev). Being checked into source control is
// exactly why it must never be relied on outside dev — production sets
// LOTUS_KEK explicitly (README), so this value is never in scope there.
const devOnlyKEK = "bG90dXMtZGV2LW9ubHkta2VrLW5vdC1mb3ItcHJvZAA="

// Mode is the deployment shape this process runs in. ModeSaaS is the
// multi-tenant product: three host kinds (apex / auth / tenant), wildcard
// DNS, one organization per subdomain. ModeSingle is a self-hosted or
// local install: one origin, exactly one organization, no subdomains and
// therefore no wildcard DNS or wildcard certificate to arrange.
//
// The default is deliberately ModeSaaS so that an existing deployment that
// sets no LOTUS_MODE keeps behaving exactly as before.
const (
	ModeSaaS   = "saas"
	ModeSingle = "single"
)

type Config struct {
	Addr        string
	DatabaseURL string
	// MigrateDatabaseURL is the DSN used only by `lotus migrate`, connecting
	// as the lotus_migrate role (has DDL). DatabaseURL connects as lotus_app
	// (no DDL, subject to RLS). Falls back to DatabaseURL when unset, which is
	// fine for local dev where one role is fine.
	MigrateDatabaseURL string
	// BlobDir is where uploaded files (QA attachments) are stored.
	BlobDir       string
	Dev           bool
	SessionSecret string
	CookieSecure  bool

	// BaseDomain is the wildcard root Lotus is served under: apex is
	// BaseDomain, auth host is "auth."+BaseDomain, tenants are
	// "<slug>."+BaseDomain. No production value has been chosen yet
	// (tracked as a decision in ActiveCollab project 697) — the default
	// below is dev-only.
	BaseDomain string

	// Mode is ModeSaaS or ModeSingle; see the constants above.
	Mode string

	// URL is the absolute base URL of a single-mode install, e.g.
	// "http://localhost:8008". It replaces BaseDomain there: with no
	// subdomains there is no domain to carve up, only one origin to build
	// absolute links against. Unused in saas mode, required in single mode.
	URL string

	// KEK is the base64-encoded key-encryption-key for envelope-encrypting
	// per-organization integration credentials (task 2.2). A fixed,
	// published-in-this-source dev-only value is used when unset, so local
	// runs don't need it set — which also means that default must never be
	// reachable in production; set LOTUS_KEK there always.
	KEK string

	WorkerCount int

	// Features switches whole modules that ship in the binary but are not
	// offered on every instance; see Features.
	Features Features

	// OAuth (task 1.1). A provider with an empty ClientSecret is simply not
	// registered — see cmd/lotus — so local dev works with zero or either
	// provider configured.
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string

	// OIDC (task 4.7) is the generic path: any standards-compliant issuer,
	// discovered from OIDCIssuer, rather than a Go type per IdP. Registered
	// only when the secret is set, exactly like the two above.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string

	// OIDCTrustEmail is the operator attesting that this issuer's email
	// addresses are assigned by an administrator, so an address it reports
	// is treated as verified even when its email_verified claim is false.
	// Off by default, and it matters: a verified address is what links an
	// identity to an existing account, accepts an invitation, and claims
	// a fresh single-mode instance's Owner seat. Only turn it on for an IdP
	// whose users cannot choose their own address — a corporate Keycloak,
	// not a public sign-up realm.
	OIDCTrustEmail bool

	// SMTP (task 1.7): sends invitation emails. With SMTPHost empty, mail
	// is simply not sent — invitations still work via the copyable link
	// (task 1.6), so this is a degrade, not a hard dependency.
	SMTPHost string
	SMTPPort string
	SMTPUser string
	SMTPPass string
	SMTPFrom string
}

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
	return c.withDevPort("auth." + c.BaseDomain)
}

func (c Config) TenantHost(slug string) string {
	if c.IsSingle() {
		panic("config: TenantHost is meaningless in single mode; use BaseURL")
	}
	return c.withDevPort(slug + "." + c.BaseDomain)
}

// withDevPort appends the port the process actually listens on, but only
// in dev mode.
//
// LOTUS_BASE_DOMAIN carries no port, and production is right to leave it
// out: 443 is implicit and must not appear in a link. Locally the server
// is on :8080, so a cross-host redirect built from the bare hostname sent
// the browser to port 80 — where, on a machine running Herd, nginx or
// Valet, some other tool answers with its own 404. That made the handoff
// unusable in dev and pointed invitation links at the wrong port.
func (c Config) withDevPort(host string) string {
	if !c.Dev {
		return host
	}
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil || port == "" || port == "80" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// BaseURL is the absolute origin pre-session links are built against —
// OAuth redirect URIs and the links in invitation emails. In saas mode
// that is always the auth host, since every pre-session flow lives there;
// in single mode it is the install's one origin.
func (c Config) BaseURL() string {
	if c.IsSingle() {
		// Trimmed here rather than only in Load: a Config built directly
		// (tests, embedders) must behave identically to one loaded from the
		// environment, or "<url>/" silently yields "//login/...".
		return strings.TrimSuffix(c.URL, "/")
	}
	return c.Scheme() + "://" + c.AuthHost()
}

// TenantURL is the absolute origin of one organization's own host —
// where the handoff sends the browser for its last hop. In single mode
// there is only one origin, so it is BaseURL.
func (c Config) TenantURL(slug string) string {
	if c.IsSingle() {
		return c.BaseURL()
	}
	return c.Scheme() + "://" + c.TenantHost(slug)
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
			return fmt.Errorf("config: LOTUS_URL must be an absolute URL like http://localhost:8008, got %q", c.URL)
		}
	default:
		return fmt.Errorf("config: LOTUS_MODE must be %q or %q, got %q", ModeSaaS, ModeSingle, c.Mode)
	}

	// A configured OIDC secret with no issuer to discover from is not a
	// degraded login, it is a login button that always errors — so fail
	// the deploy instead of the first person who clicks it.
	if c.OIDCClientSecret != "" {
		u, err := url.Parse(c.OIDCIssuer)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("config: OIDC_ISSUER must be an absolute URL like "+
				"https://sso.example.com/realms/internal when OIDC_CLIENT_SECRET is set, got %q", c.OIDCIssuer)
		}
	}
	return nil
}

// Scheme is "https" outside dev mode, "http" in it — used to build
// absolute URLs (OAuth redirect URIs) that need a real scheme, not just a
// host.
func (c Config) Scheme() string {
	if c.CookieSecure {
		return "https"
	}
	return "http"
}

// Load builds the config from environment variables.
//
// In production the process is configured entirely via real environment
// variables (systemd EnvironmentFile, rendered from GitLab CI/CD variables),
// where LOTUS_DEV=0 is set — so no .env file is ever read on the server. The
// .env load below is a local-dev convenience only, skipped whenever
// LOTUS_DEV=0 is already present in the environment.
func Load() Config {
	if os.Getenv("LOTUS_DEV") != "0" {
		loadDotEnv(".env")
	}
	c := Config{
		Addr:               envOr("ADDR", ":8080"),
		DatabaseURL:        envOr("DATABASE_URL", "postgres://postgres@localhost:5418/lotus?sslmode=disable"),
		MigrateDatabaseURL: os.Getenv("MIGRATE_DATABASE_URL"),
		BlobDir:            envOr("LOTUS_BLOB_DIR", "var/blob"),
		Dev:                os.Getenv("LOTUS_DEV") == "1",
		SessionSecret:      os.Getenv("SESSION_SECRET"),
		BaseDomain:         envOr("LOTUS_BASE_DOMAIN", "lotus.localhost"),
		Mode:               envOr("LOTUS_MODE", ModeSaaS),
		URL:                strings.TrimSuffix(os.Getenv("LOTUS_URL"), "/"),
		KEK:                envOr("LOTUS_KEK", devOnlyKEK),
		WorkerCount:        envInt("LOTUS_WORKER_COUNT", 4),
		Features:           Features{QA: envFlag("LOTUS_FEATURE_QA")},
		GitHubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		OIDCIssuer:         strings.TrimSuffix(os.Getenv("OIDC_ISSUER"), "/"),
		OIDCClientID:       os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:   os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCTrustEmail:     os.Getenv("OIDC_TRUST_EMAIL") == "1" || os.Getenv("OIDC_TRUST_EMAIL") == "true",
		SMTPHost:           os.Getenv("SMTP_HOST"),
		SMTPPort:           envOr("SMTP_PORT", "587"),
		SMTPUser:           os.Getenv("SMTP_USER"),
		SMTPPass:           os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:           envOr("SMTP_FROM", "Lotus <no-reply@lotus.invalid>"),
	}
	if c.MigrateDatabaseURL == "" {
		c.MigrateDatabaseURL = c.DatabaseURL
	}
	if c.SessionSecret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		c.SessionSecret = hex.EncodeToString(b)
	}
	// Session cookies are Secure (HTTPS-only) in production; defaults off in
	// dev (plain-HTTP localhost). Override with COOKIE_SECURE=1/0.
	c.CookieSecure = !c.Dev
	if v := os.Getenv("COOKIE_SECURE"); v != "" {
		c.CookieSecure = v == "1" || v == "true"
	}
	return c
}

// Features gates modules that are built and tested but not yet offered.
// Every one is off unless its LOTUS_FEATURE_* variable is set, so a module
// reaches users through a deliberate decision on each instance rather than
// by being merged. Off means absent, not disabled: its routes are never
// registered, so its pages, API and MCP server all answer 404, and nothing
// else in the app links to it.
type Features struct {
	// QA is QA Tools — test cases, scenarios, plans, runs, issues, project
	// guests, the QA JSON API and its MCP server. LOTUS_FEATURE_QA.
	QA bool
}

// envFlag reads a boolean switch the way COOKIE_SECURE is read: "1" or
// "true" turns it on, anything else (including unset) leaves it off.
func envFlag(k string) bool {
	v := os.Getenv(k)
	return v == "1" || v == "true"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}
