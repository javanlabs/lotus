package web

import (
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

// TestRouteCoverage is task 1.5: every registered route must carry an
// explicit authz.Require(...) or authz.Public("reason") — enumerated the
// same way task 1.5 asks, via app.GetRoutes(), so a route added without
// one fails this test by name (method + path), not silently in
// production. This is deliberately the smallest possible task in the
// project and, per its own description, plausibly the most valuable: this
// is what catches the next unauthenticated write endpoint before it ships.
func TestRouteCoverage(t *testing.T) {
	// Both modes: single mode registers a different route set, and a route
	// correctly guarded in one mode could be bare in the other. Running the
	// scan twice is what turns that risk into a CI failure rather than a
	// review burden.
	for _, mode := range []string{config.ModeSaaS, config.ModeSingle} {
		t.Run(mode, func(t *testing.T) {
			app := buildFullAppForCoverage(t, mode)

			violations, publicRoutes := scanRouteCoverage(app)

			// The /static mount is registered via app.Use, so app.GetRoutes(true)
			// (used inside scanRouteCoverage) excludes it — check it separately.
			staticOK := false
			for _, route := range app.GetRoutes(false) {
				if route.Path != "/static" {
					continue
				}
				if kind, ok := decisionFor(route.Handlers); ok {
					staticOK = true
					if kind == "public" {
						publicRoutes = append(publicRoutes, "USE /static/*")
					}
					break
				}
			}
			if !staticOK {
				violations = append(violations, "USE /static")
			}

			t.Logf("public routes (%d):\n%s", len(publicRoutes), strings.Join(dedupe(publicRoutes), "\n"))

			if len(violations) > 0 {
				t.Fatalf("routes with no authz.Require/authz.Public decision:\n%s", strings.Join(violations, "\n"))
			}
		})
	}
}

// TestRouteCoverage_CatchesABareRoute proves the scan above can actually
// fail — a coverage check that can never fail proves nothing (the same
// principle task 5.3 states for the cross-tenant fixture suite).
func TestRouteCoverage_CatchesABareRoute(t *testing.T) {
	app := fiber.New()
	app.Get("/decided", authz.Public("test fixture"), func(c *fiber.Ctx) error { return nil })
	app.Get("/undecided", func(c *fiber.Ctx) error { return nil })

	violations, _ := scanRouteCoverage(app)
	if len(violations) != 1 || violations[0] != "GET /undecided" {
		t.Fatalf("violations = %v, want exactly [\"GET /undecided\"]", violations)
	}
}

// scanRouteCoverage is the coverage check itself, factored out so it can
// be exercised against both the real app and a minimal fixture (above).
func scanRouteCoverage(app *fiber.App) (violations, publicRoutes []string) {
	for _, route := range app.GetRoutes(true) { // true: exclude raw app.Use() bookkeeping entries
		if route.Method == fiber.MethodHead {
			continue // HEAD mirrors GET's own handler chain; nothing new to check
		}
		kind, ok := decisionFor(route.Handlers)
		if !ok {
			violations = append(violations, route.Method+" "+route.Path)
			continue
		}
		if kind == "public" {
			publicRoutes = append(publicRoutes, route.Method+" "+route.Path)
		}
	}
	return violations, publicRoutes
}

func decisionFor(handlers []fiber.Handler) (string, bool) {
	for _, h := range handlers {
		if kind, ok := authz.RouteDecision(h); ok {
			return kind, ok
		}
	}
	return "", false
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func buildFullAppForCoverage(t *testing.T, mode string) *fiber.App {
	t.Helper()
	db := dbtest.Open(t)
	renderer := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	cfg := config.Config{Mode: mode, BaseDomain: "lotus.test", URL: "http://localhost:8008", Dev: true}
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
