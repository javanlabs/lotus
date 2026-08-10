// Package web wires Lotus's HTTP routes.
package web

import (
	"io/fs"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/tenant"
)

// Deps are every dependency the route table needs. Grouped into a struct
// because the list only grows as each Fase 1 task adds routes.
type Deps struct {
	Renderer       *httpx.Renderer
	StaticFS       fs.FS
	Resolver       *tenant.Resolver
	DB             *sqlx.DB
	Cfg            config.Config
	OAuthProviders authn.Registry
	CookieSecure   bool
	Mailer         *mailer.Mailer
}

// New builds the Fiber app. Every route carries an explicit authz.Require
// or authz.Public marker (task 1.5) — internal/web's own route-coverage
// test fails the build the moment one doesn't. /healthz and the Fase-0
// sample page are deliberately host-agnostic — they exist for local dev
// and uptime checks, not as part of the apex/auth/tenant taxonomy task 0.5
// establishes for the rest of the route table.
func New(d Deps) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "lotus",
	})
	app.Use(recover.New())
	app.Use(logger.New())
	// Every route below this line is guaranteed to run after the
	// resolver — task 1.5's "no route before resolver + auth middleware"
	// invariant holds structurally, by nothing being registered above it.
	app.Use(d.Resolver.Middleware())

	app.Use("/static", authz.Public("static assets: no request-specific data, safe on every host"),
		filesystem.New(filesystem.Config{Root: http.FS(d.StaticFS)}))

	app.Get("/healthz", authz.Public("uptime check; must work without DNS/host setup"), healthz)
	app.Get("/", authz.Public("Fase-0 sample page; becomes the apex marketing page in task 4.1"), home(d.Renderer))

	authDeps := authn.Deps{
		DB:             d.DB,
		Cfg:            d.Cfg,
		Renderer:       d.Renderer,
		OAuthProviders: d.OAuthProviders,
		CookieSecure:   d.CookieSecure,
		Mailer:         d.Mailer,
	}

	// Auth host only (tasks 1.1/1.2). RequireAuth 404s these on any tenant
	// subdomain or the apex, closing the cross-host bug class task 0.5
	// exists to prevent. Public: these endpoints exist specifically to
	// establish a session — requiring one first would be a deadlock.
	app.Get("/login/:provider", tenant.RequireAuth(),
		authz.Public("starts OAuth; there is no session yet by definition"), authn.Login(authDeps))
	app.Get("/login/:provider/callback", tenant.RequireAuth(),
		authz.Public("OAuth provider redirect target; establishes the session"), authn.Callback(authDeps))
	app.Get("/handoff/authorize", tenant.RequireAuth(),
		authz.Public("handoff step 2/3; runs before the tenant session exists"), authn.HandoffAuthorize(authDeps))

	// Tenant host only (task 1.2). Same reasoning: these establish the
	// tenant session, so they can't require one first.
	app.Get("/handoff/start", tenant.RequireTenant(),
		authz.Public("handoff step 1; starts the flow that creates the tenant session"), authn.HandoffStart(authDeps))
	app.Get("/handoff/finish", tenant.RequireTenant(),
		authz.Public("handoff step 4; creates the tenant session"), authn.HandoffFinish(authDeps))

	// Works on either host (task 1.3): revokes whichever session cookie —
	// global or tenant — is present. A no-op on the apex, which never has
	// either cookie.
	app.Post("/logout", authz.Public("must be reachable without a valid session to be useful at all"), authn.Logout(authDeps))

	// Auth host only (task 1.6): signup and onboarding, gated by a global
	// session rather than an org permission — there's no organization yet
	// for most of this flow, so authz.Require doesn't apply; Public here
	// documents that RequireGlobalSession (not a permission check) is the
	// actual gate. /onboarding/invite's own org-membership check happens
	// inside the handler (see requireOwnedOrg) since it depends on the
	// ?org= query param, not something a static route guard can express.
	app.Get("/signup", tenant.RequireAuth(),
		authz.Public("pre-session by definition; the entry point into OAuth"), authn.Signup(authDeps))
	app.Get("/welcome", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("gated by RequireGlobalSession, not an org permission"), authn.Welcome(authDeps))
	app.Post("/welcome", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("gated by RequireGlobalSession, not an org permission"), authn.SubmitWelcome(authDeps))
	app.Get("/onboarding/org", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("gated by RequireGlobalSession, not an org permission"), authn.OnboardingOrg(authDeps))
	app.Get("/onboarding/slug-check", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("gated by RequireGlobalSession, not an org permission"), authn.SlugCheck(authDeps))
	app.Post("/onboarding/org", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("gated by RequireGlobalSession, not an org permission"), authn.SubmitOnboardingOrg(authDeps))
	app.Get("/onboarding/invite", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("membership+permission checked inside the handler against ?org="), authn.OnboardingInvite(authDeps))
	app.Post("/onboarding/invite", tenant.RequireAuth(), authn.RequireGlobalSession(authDeps),
		authz.Public("membership+permission checked inside the handler against ?org="), authn.SubmitOnboardingInvite(authDeps))

	// Auth host only (task 1.7): the invitation accept/decline flow.
	// Public: an invitee has no session yet by definition, and decline
	// deliberately never requires one.
	app.Get("/invite/:token", tenant.RequireAuth(),
		authz.Public("pre-session by definition; shows the accept/login prompt"), authn.InviteLanding(authDeps))
	app.Post("/invite/:token/confirm", tenant.RequireAuth(),
		authz.Public("explicit user-confirmed accept for a mismatched-email invite"), authn.InviteConfirm(authDeps))
	app.Post("/invite/:token/decline", tenant.RequireAuth(),
		authz.Public("declining must not require a login the person never wanted"), authn.InviteDecline(authDeps))

	return app
}

func healthz(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func home(renderer *httpx.Renderer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Type("html")
		return renderer.Render(c, "pages/home.html", httpx.ViewData{Title: "Home"})
	}
}
