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
			cfg: config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008"},
		},
		"single without url": {
			cfg:     config.Config{Mode: config.ModeSingle},
			wantErr: "LOTUS_URL",
		},
		"single with a bare host, not a URL": {
			cfg:     config.Config{Mode: config.ModeSingle, URL: "localhost:8008"},
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
	single := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008/"}
	if got, want := single.BaseURL(), "http://localhost:8008"; got != want {
		t.Errorf("single BaseURL() = %q, want %q", got, want)
	}
}

// TestHostHelpersPanicInSingleMode pins the mode assertion: AuthHost and
// TenantHost describe a subdomain layout that does not exist in single
// mode, so calling them there is a programming error that must surface as
// a test failure, not as a malformed link in production.
func TestHostHelpersPanicInSingleMode(t *testing.T) {
	single := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008"}
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
	if !(config.Config{Mode: config.ModeSingle}).IsSingle() {
		t.Error("IsSingle() = false for ModeSingle")
	}
	if (config.Config{Mode: config.ModeSaaS}).IsSingle() {
		t.Error("IsSingle() = true for ModeSaaS")
	}
}

// TestDevHostsCarryTheListeningPort covers the bug that made cross-host
// login impossible locally: LOTUS_BASE_DOMAIN has no port, so a redirect
// built from the bare hostname sent the browser to port 80 — a machine
// running Herd or nginx answers there with something that is not Lotus.
func TestDevHostsCarryTheListeningPort(t *testing.T) {
	dev := config.Config{Mode: config.ModeSaaS, BaseDomain: "lotus.localhost", Addr: ":8080", Dev: true}
	if got, want := dev.AuthHost(), "auth.lotus.localhost:8080"; got != want {
		t.Fatalf("dev AuthHost() = %q, want %q", got, want)
	}
	if got, want := dev.TenantHost("acme"), "acme.lotus.localhost:8080"; got != want {
		t.Fatalf("dev TenantHost() = %q, want %q", got, want)
	}
	if got, want := dev.TenantURL("acme"), "http://acme.lotus.localhost:8080"; got != want {
		t.Fatalf("dev TenantURL() = %q, want %q", got, want)
	}

	// Production must not gain a port: 443 is implicit and a link that
	// spells it out is wrong.
	// CookieSecure is what makes Scheme() https — the production shape.
	prod := config.Config{Mode: config.ModeSaaS, BaseDomain: "lotus.com", Addr: ":8080", CookieSecure: true}
	if got, want := prod.AuthHost(), "auth.lotus.com"; got != want {
		t.Fatalf("prod AuthHost() = %q, want %q", got, want)
	}
	if got, want := prod.TenantURL("acme"), "https://acme.lotus.com"; got != want {
		t.Fatalf("prod TenantURL() = %q, want %q", got, want)
	}

	// Port 80 in dev is the same implicit case, and an unparseable Addr
	// must degrade to the bare host rather than produce a broken link.
	for _, addr := range []string{":80", "", "nonsense"} {
		c := config.Config{Mode: config.ModeSaaS, BaseDomain: "lotus.localhost", Addr: addr, Dev: true}
		if got, want := c.AuthHost(), "auth.lotus.localhost"; got != want {
			t.Fatalf("AuthHost() with Addr %q = %q, want %q", addr, got, want)
		}
	}
}

// Single mode has one origin; the handoff's tenant hop collapses onto it.
func TestTenantURLInSingleMode(t *testing.T) {
	single := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008"}
	if got, want := single.TenantURL("ignored"), "http://localhost:8008"; got != want {
		t.Fatalf("TenantURL() = %q, want %q", got, want)
	}
}

// LOTUS_FEATURE_QA is off unless set to exactly "1" or "true" — the same
// rule COOKIE_SECURE uses. An unrecognised value must not switch a module
// on, so a typo leaves it hidden rather than exposing it.
func TestFeatureQAIsOffUnlessExplicitlyEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"yes":   false,
		"on":    false,
		"1":     true,
		"true":  true,
	}
	for value, want := range cases {
		t.Run("value="+value, func(t *testing.T) {
			// LOTUS_DEV=0 stops Load reading a developer's .env, which
			// could set the flag and make this test depend on the machine.
			t.Setenv("LOTUS_DEV", "0")
			t.Setenv("LOTUS_FEATURE_QA", value)
			if got := config.Load().Features.QA; got != want {
				t.Errorf("LOTUS_FEATURE_QA=%q: Features.QA = %v, want %v", value, got, want)
			}
		})
	}
}
