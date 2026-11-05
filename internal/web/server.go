// Package web wires Lotus's HTTP routes.
package web

import (
	"context"
	"io/fs"
	"net"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
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
	KEK            *crypto.KEK
	// Blob stores uploaded files (QA attachments); config LOTUS_BLOB_DIR.
	// Nil when it could not be created: file routes then answer 503.
	Blob blob.Store

	// NetguardResolver overrides task 2.2's SSRF-guarded HTTP client's DNS
	// resolution. Nil in every real deployment (real DNS). Tests set this
	// to a fake resolver so connection-creation tests don't depend on
	// outbound network access — see internal/netguard.Config's own doc
	// comment for why this needs to be injectable at all.
	NetguardResolver func(ctx context.Context, host string) ([]net.IP, error)

	// NetguardIsBlocked overrides which resolved IPs the SSRF guard
	// rejects. Nil in every real deployment (the real, restrictive
	// default). Task 2.4's discovery tests need to point the guarded
	// client at a real httptest.Server, which always binds loopback — see
	// internal/netguard.Config's own doc comment for why this needs to be
	// injectable, same reasoning as NetguardResolver.
	NetguardIsBlocked func(net.IP) bool
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
		// QA attachments go up to repo.MaxQAAttachmentBytes (20 MB) per
		// file; Fiber's 4 MB default would refuse them before any handler
		// could say why. The margin covers the multipart envelope and a
		// comment beside the file. The body is read whole before any
		// middleware runs, so in production nginx keeps every other route
		// at 4m (deploy/nginx-lotuscheck.conf).
		BodyLimit:    25 << 20,
		ErrorHandler: errorHandler(d),
	})
	app.Use(recover.New())
	app.Use(requestLogger(nil))
	// Every route below this line is guaranteed to run after the
	// resolver — task 1.5's "no route before resolver + auth middleware"
	// invariant holds structurally, by nothing being registered above it.
	app.Use(d.Resolver.Middleware())
	app.Use(errorPages(d))

	// In dev the assets are read from disk on every request, but the
	// browser still caches them — so an edited stylesheet keeps rendering
	// the old rules until someone thinks to hard-reload, which reads as a
	// broken change rather than a stale cache. Opt out of caching there;
	// production keeps the default (immutable, fingerprinted-by-build
	// assets that should cache).
	if d.Cfg.Dev {
		app.Use("/static", func(c *fiber.Ctx) error {
			c.Set("Cache-Control", "no-store")
			return c.Next()
		})
	}
	app.Use("/static", authz.Public("static assets: no request-specific data, safe on every host"),
		filesystem.New(filesystem.Config{Root: http.FS(d.StaticFS)}))

	app.Get("/healthz", authz.Public("uptime check; must work without DNS/host setup"), healthz)
	if d.Cfg.IsSingle() {
		// One origin means there is no separate marketing surface to land
		// on: / is the app. Redirecting rather than rendering the dashboard
		// here keeps a single canonical URL for it.
		app.Get("/", authz.Public("redirect only; the dashboard behind it enforces its own session"),
			func(c *fiber.Ctx) error { return c.Redirect("/dashboard", fiber.StatusFound) })
	} else {
		// An organization's own host is the app, as in single mode; sign-in
		// returns to / and lands on the dashboard. The apex (and the auth
		// host) keep the placeholder.
		landing := home(d.Renderer)
		app.Get("/", authz.Public("apex placeholder, or a redirect to the dashboard, which enforces its own session"),
			func(c *fiber.Ctx) error {
				if _, ok := tenant.OrgFromContext(c); ok {
					return c.Redirect("/dashboard", fiber.StatusFound)
				}
				return landing(c)
			})
	}

	authDeps := authn.Deps{
		DB:             d.DB,
		Cfg:            d.Cfg,
		Renderer:       d.Renderer,
		OAuthProviders: d.OAuthProviders,
		CookieSecure:   d.CookieSecure,
		Mailer:         d.Mailer,
	}

	// Works on either host (task 1.3): revokes whichever session cookie —
	// global or tenant — is present. A no-op on the apex, which never has
	// either cookie.
	app.Post("/logout", authz.Public("must be reachable without a valid session to be useful at all"), authn.Logout(authDeps))

	// The auth host, the handoff, and self-serve onboarding all exist only
	// because saas mode splits one product across three hosts and lets
	// anyone create an organization. A single-mode install has one origin
	// and one organization created by `lotus init`, so those routes are not
	// registered at all — the route table itself documents what exists
	// where, instead of a runtime 404 that looks like a bug.
	if d.Cfg.IsSingle() {
		registerSingleModeAuthRoutes(app, authDeps, d.Cfg.Dev)
	} else {
		registerSaaSAuthRoutes(app, authDeps)
	}

	// Tenant host only (task 4.1/4.2): the app shell's dashboard, plus a
	// minimal real project-creation page (name+slug only — the fuller
	// wizard that also adds GitLab repos and a Sentry DSN is task 4.3's
	// bind-metric wizard).
	registerDashboardRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
	registerProjectPageRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 4.3): a project's own page, adding a
	// component to it, a component's own page (its metric bindings), and
	// the bind-metric wizard that creates a connection (if needed) and a
	// binding together.
	registerProjectPageDetailRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
	registerProjectTagRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
	// A project's page per integration — the KPI cards on the project page
	// open these.
	registerProjectMetricRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (QA module, docs/superpowers/plans/2026-09-27-qa-phase-4.md):
	// the QA overview and each project's QA area. Behind LOTUS_FEATURE_QA,
	// along with its API and MCP server below: not registered at all when
	// off, so every QA URL — including a share link already handed out —
	// answers 404 rather than reaching a handler that has to remember to
	// refuse.
	if d.Cfg.Features.QA {
		registerQARoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
	}
	registerComponentPageRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
	registerBindWizardRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 4.6): each component-page binding row's own
	// htmx-lazy-loaded sparkline — kept separate from
	// registerComponentPageRoutes above since it's a follow-up fragment
	// request per row, not part of the page's own first render.
	registerSparklineRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 4.4): the four integration-specific metric
	// detail pages, keyed by binding_id — /p/:project/c/:component/m/:binding
	// — plus that binding's own run history and (Trivy only) its
	// false-positive annotations.
	registerMetricDetailRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 4.5): the organization administration
	// surface — Connections, Members, Organization, Tokens, Ingest docs,
	// and the org-wide Activity feed. SSO (/settings/sso) is not part of
	// this — deferred to its own follow-up task.
	registerSettingsRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 2.1): Project & Component CRUD. JSON handlers
	// — the screens that call them are task 4.3.
	registerProjectRoutes(app, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 2.2): Connection CRUD, secret rotation, and
	// Test Connection. JSON handlers — the screens are task 4.5.
	registerConnectionRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 2.3): Metric binding CRUD, descriptor-driven
	// config validation, and Test Binding. JSON handlers — the wizard UI
	// is task 4.3.
	registerBindingRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 2.4): Discovery + the unclaimed-resources
	// panel + bulk claim. JSON handlers — the wizard UI is task 4.3.
	registerDiscoveryRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	// Tenant host only (task 3.4): push-direction Trivy ingest, authenticated
	// by a service token — see internal/web/ingest.go's own doc comment.
	registerScoringDocsRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))

	registerIngestRoutes(app, d, authDeps)
	if d.Cfg.Features.QA {
		registerQAAPIRoutes(app, authDeps)
		registerQAMCPRoutes(app, authDeps)
	}

	return app
}

func healthz(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"ok": true})
}

func home(renderer *httpx.Renderer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Type("html")
		// A failed sign-in in saas mode lands here (the auth host has no
		// login page of its own), so the notice is shown here too.
		return renderer.Render(c, "pages/home.html", httpx.ViewData{
			Title: "Home",
			Data:  struct{ Notice string }{authn.LoginNotice(c.Query("notice"))},
		})
	}
}

// registerSaaSAuthRoutes registers everything that only makes sense when
// the auth host and the tenant hosts are different origins.
func registerSaaSAuthRoutes(app *fiber.App, authDeps authn.Deps) {
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
}

// registerSingleModeAuthRoutes registers the subset that still applies on
// one origin: OAuth/OIDC login itself, and the invitation flow that is how
// members join an install whose organization already exists. There is no
// handoff (nothing to bridge) and no signup (the org comes from
// `lotus init`).
func registerSingleModeAuthRoutes(app *fiber.App, authDeps authn.Deps, devMode bool) {
	app.Get("/login", tenant.RequireAuth(),
		authz.Public("the login page itself; a session is what it exists to create"), authn.LoginPage(authDeps))
	// Magic link: the primary way in when no OAuth provider is configured.
	// Both are Public for the same reason every login route is — a session
	// is the thing they exist to create.
	app.Post("/login/email", tenant.RequireTenant(),
		authz.Public("requests a login link; answers identically for members and strangers"), authn.RequestMagicLink(authDeps))
	app.Get("/login/email/verify", tenant.RequireTenant(),
		authz.Public("redeems a single-use login link and establishes the session"), authn.VerifyMagicLink(authDeps))

	// Dev-only one-click login as a seeded @lotus.test account. Gate one of
	// three: the route does not exist at all outside LOTUS_DEV, so there is
	// nothing to reach even with a correct URL. See authn.DevLoginAs for
	// the other two.
	if devMode {
		app.Post("/login/dev/:user", tenant.RequireTenant(),
			authz.Public("dev-only impersonation of a seeded test account; see authn.DevLoginAs"), authn.DevLoginAs(authDeps))
	}

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
