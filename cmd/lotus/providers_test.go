package main

import (
	"testing"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
)

// The registry is the seam between "the operator set some environment
// variables" and "the login page offers a button". Nothing else covers it:
// the provider types are unit-tested, the template renders whatever it is
// given, and in between sits this function, where a missed condition means
// a correctly configured IdP that simply never appears.
func TestOAuthProvidersRegistersOnlyWhatIsConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{
			name: "nothing configured",
			cfg:  config.Config{Mode: config.ModeSingle, URL: "https://lotus.example"},
			want: nil,
		},
		{
			name: "oidc only — the shape this instance actually deploys",
			cfg: config.Config{
				Mode: config.ModeSingle, URL: "https://lotus.example",
				OIDCIssuer:   "https://sso.example/realms/internal",
				OIDCClientID: "lotus", OIDCClientSecret: "shh",
			},
			want: []string{"sso"},
		},
		{
			name: "an issuer with no secret registers nothing",
			cfg: config.Config{
				Mode: config.ModeSingle, URL: "https://lotus.example",
				OIDCIssuer: "https://sso.example/realms/internal",
			},
			want: nil,
		},
		{
			name: "all three coexist",
			cfg: config.Config{
				Mode: config.ModeSingle, URL: "https://lotus.example",
				GitHubClientID: "g", GitHubClientSecret: "s",
				GoogleClientID: "g", GoogleClientSecret: "s",
				OIDCIssuer:   "https://sso.example/realms/internal",
				OIDCClientID: "lotus", OIDCClientSecret: "shh",
			},
			want: []string{"github", "google", "sso"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oauthProviders(tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("registered %d provider(s) %v, want %v", len(got), keys(got), tt.want)
			}
			for _, name := range tt.want {
				if _, ok := got[name]; !ok {
					t.Errorf("provider %q not registered; got %v", name, keys(got))
				}
			}
		})
	}
}

// The IdP has to be told one exact callback URL, and getting it wrong
// fails only at the very end of a real login round-trip, with an
// invalid_redirect_uri from the IdP rather than anything Lotus logs. Pin
// it: changing this means editing every deployed instance's IdP client.
func TestOIDCRedirectURLIsTheDocumentedCallback(t *testing.T) {
	cfg := config.Config{
		Mode: config.ModeSingle, URL: "https://lotus.javan.id",
		OIDCIssuer:   "https://sso.javan.id/realms/internal",
		OIDCClientID: "lotus", OIDCClientSecret: "shh",
	}
	p, ok := oauthProviders(cfg)["sso"]
	if !ok {
		t.Fatal("sso provider not registered")
	}
	if p.Name() != "sso" {
		t.Errorf("Name() = %q, want the URL segment the route table uses", p.Name())
	}
	oidc, ok := p.(*authn.OIDCProvider)
	if !ok {
		t.Fatalf("registered provider is %T, want *authn.OIDCProvider", p)
	}
	const want = "https://lotus.javan.id/login/sso/callback"
	if oidc.RedirectURL != want {
		t.Errorf("RedirectURL = %q, want %q — this is the value whitelisted on the IdP client",
			oidc.RedirectURL, want)
	}
	if oidc.Issuer != "https://sso.javan.id/realms/internal" {
		t.Errorf("Issuer = %q", oidc.Issuer)
	}
	if oidc.ClientID != "lotus" {
		t.Errorf("ClientID = %q", oidc.ClientID)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
