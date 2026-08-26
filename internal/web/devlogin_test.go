package web

import (
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

func buildSingleModeApp(t *testing.T, dev bool) *fiber.App {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{Mode: config.ModeSingle, URL: "http://localhost:8008", Dev: dev}
	return New(Deps{
		Renderer:       httpx.NewRenderer(os.DirFS("../../web/templates"), true),
		StaticFS:       os.DirFS("../../web/static"),
		Resolver:       tenant.NewResolver(cfg, db),
		DB:             db,
		Cfg:            cfg,
		OAuthProviders: authn.Registry{},
	})
}

func routeExists(app *fiber.App, method, path string) bool {
	for _, r := range app.GetRoutes(true) {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// TestDevLoginRouteOnlyExistsInDevMode is the outermost of the three gates
// around one-click impersonation. It is structural rather than a runtime
// check: with LOTUS_DEV unset the route is never registered, so there is
// nothing to reach even with a correct URL and a valid user id.
//
// If this test ever fails, a build that can impersonate an account without
// any credential is one deploy away from production.
func TestDevLoginRouteOnlyExistsInDevMode(t *testing.T) {
	const method, path = fiber.MethodPost, "/login/dev/:user"

	if !routeExists(buildSingleModeApp(t, true), method, path) {
		t.Fatal("dev mode: route is missing, so the seeded-account shortcut cannot work at all")
	}
	if routeExists(buildSingleModeApp(t, false), method, path) {
		t.Fatal("LOTUS_DEV unset: /login/dev/:user IS REGISTERED — one-click impersonation is reachable outside dev")
	}
}

// The magic-link routes, unlike the dev shortcut, exist in both — they are
// a real login mechanism, not a testing affordance.
func TestMagicLinkRoutesExistRegardlessOfDevMode(t *testing.T) {
	for _, dev := range []bool{true, false} {
		app := buildSingleModeApp(t, dev)
		for _, r := range []struct{ method, path string }{
			{fiber.MethodPost, "/login/email"},
			{fiber.MethodGet, "/login/email/verify"},
		} {
			if !routeExists(app, r.method, r.path) {
				t.Errorf("dev=%v: %s %s is not registered", dev, r.method, r.path)
			}
		}
	}
}
