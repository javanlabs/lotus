package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

type fakeSSO struct{}

func (fakeSSO) Name() string { return "sso" }
func (fakeSSO) AuthCodeURL(state, challenge string) (string, error) {
	return "https://sso.example/auth", nil
}
func (fakeSSO) Exchange(context.Context, string, string) (string, error) {
	return "", nil
}
func (fakeSSO) FetchIdentity(context.Context, string) (authn.Identity, error) {
	return authn.Identity{}, nil
}

func loginPage(t *testing.T, providers authn.Registry) string {
	t.Helper()
	db := dbtest.Open(t)
	if _, err := db.Exec(`INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan Cipta Solusi')`); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008"}
	app := New(Deps{
		Renderer: httpx.NewRenderer(os.DirFS("../../web/templates"), true), StaticFS: os.DirFS("../../web/static"),
		Resolver: tenant.NewResolver(cfg, db), DB: db, Cfg: cfg, OAuthProviders: providers,
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "http://localhost:8008/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page = %d", resp.StatusCode)
	}
	return string(body)
}

// For now login is SSO only: the email (magic link) form is hidden while an
// SSO provider is configured. The page carries the Lotus mark and tagline.
func TestLoginPage_SSOOnlyWithTheLotusBrand(t *testing.T) {
	page := loginPage(t, authn.Registry{"sso": fakeSSO{}})
	if strings.Contains(page, `action="/login/email"`) {
		t.Errorf("the email login form is shown next to SSO")
	}
	for _, want := range []string{`href="/login/sso?return=`, "Continue with SSO", "every metric, surfaced", "Javan Cipta Solusi", `<svg viewBox="18 44 164 106"`} {
		if !strings.Contains(page, want) {
			t.Errorf("login page lacks %q", want)
		}
	}
}

// Without any SSO provider, hiding the email form would leave no way in:
// then it stays.
func TestLoginPage_EmailFormWhenNoSSO(t *testing.T) {
	if page := loginPage(t, authn.Registry{}); !strings.Contains(page, `action="/login/email"`) {
		t.Errorf("no SSO configured and no email form: nobody can log in")
	}
}
