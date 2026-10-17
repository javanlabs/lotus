package authn_test

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/tenant"
)

func newOnboardingTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, Renderer: testRenderer(t), OAuthProviders: authn.Registry{}, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/welcome", authn.RequireGlobalSession(deps), authn.Welcome(deps))
	app.Post("/welcome", authn.RequireGlobalSession(deps), authn.SubmitWelcome(deps))
	app.Get("/onboarding/org", authn.RequireGlobalSession(deps), authn.OnboardingOrg(deps))
	app.Get("/onboarding/slug-check", authn.RequireGlobalSession(deps), authn.SlugCheck(deps))
	app.Post("/onboarding/org", authn.RequireGlobalSession(deps), authn.SubmitOnboardingOrg(deps))
	app.Get("/onboarding/invite", authn.RequireGlobalSession(deps), authn.OnboardingInvite(deps))
	app.Post("/onboarding/invite", authn.RequireGlobalSession(deps), authn.SubmitOnboardingInvite(deps))
	app.Get("/handoff/start", tenant.RequireTenant(), authn.HandoffStart(deps))
	app.Get("/handoff/authorize", tenant.RequireAuth(), authn.HandoffAuthorize(deps))
	app.Get("/handoff/finish", tenant.RequireTenant(), authn.HandoffFinish(deps))
	return app, db, cfg
}

func seedOnboardingUser(t *testing.T, db *sqlx.DB) (uuid.UUID, string) {
	t.Helper()
	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ($1, now()) RETURNING id`, uuid.New().String()+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	raw := seedGlobalSession(t, db, userID)
	return userID, raw
}

func TestOnboarding_FullFlowLandsOnTenant(t *testing.T) {
	t.Parallel()
	app, db, cfg := newOnboardingTestApp(t)
	_, sessionRaw := seedOnboardingUser(t, db)
	authCookie := &http.Cookie{Name: "lotus_auth", Value: sessionRaw}

	// /welcome
	welcomeReq := hostFormReq("auth."+cfg.BaseDomain, "/welcome", "name=Alice")
	welcomeReq.AddCookie(authCookie)
	welcomeResp, err := app.Test(welcomeReq, -1)
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	if welcomeResp.StatusCode != http.StatusFound {
		t.Fatalf("welcome status = %d, want 302", welcomeResp.StatusCode)
	}

	// POST /onboarding/org
	orgReq := hostFormReq("auth."+cfg.BaseDomain, "/onboarding/org", "name=Acme&slug=acme-onboard")
	orgReq.AddCookie(authCookie)
	orgResp, err := app.Test(orgReq, -1)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	if orgResp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(orgResp.Body)
		t.Fatalf("create org status = %d, want 302: %s", orgResp.StatusCode, body)
	}
	loc, _ := orgResp.Location()
	if loc.Path != "/onboarding/invite" {
		t.Fatalf("redirected to %q, want /onboarding/invite", loc.Path)
	}

	var ownerCheck int
	if err := db.Get(&ownerCheck, `
		SELECT count(*) FROM memberships m JOIN roles r ON r.id = m.role_id
		WHERE r.key = 'owner' AND m.org_id = (SELECT id FROM organizations WHERE subdomain = 'acme-onboard')`); err != nil {
		t.Fatalf("check owner: %v", err)
	}
	if ownerCheck != 1 {
		t.Fatalf("expected creator to be Owner, got %d owner rows", ownerCheck)
	}

	// POST /onboarding/invite — skip.
	inviteReq := hostFormReq("auth."+cfg.BaseDomain, "/onboarding/invite?org=acme-onboard", "action=skip")
	inviteReq.AddCookie(authCookie)
	inviteResp, err := app.Test(inviteReq, -1)
	if err != nil {
		t.Fatalf("skip invite: %v", err)
	}
	if inviteResp.StatusCode != http.StatusFound {
		t.Fatalf("skip invite status = %d, want 302", inviteResp.StatusCode)
	}
	handoffStartURL, err := inviteResp.Location()
	if err != nil {
		t.Fatalf("no redirect from invite: %v", err)
	}
	if !strings.HasPrefix(handoffStartURL.Host, "acme-onboard.") {
		t.Fatalf("expected redirect to acme-onboard tenant host, got %q", handoffStartURL.Host)
	}

	// Follow the handoff: start -> authorize (reuses the global session,
	// now a member) -> finish.
	startReq := hostReq(http.MethodGet, handoffStartURL.Host, handoffStartURL.RequestURI())
	startResp, err := app.Test(startReq, -1)
	if err != nil {
		t.Fatalf("handoff start: %v", err)
	}
	authorizeURL, err := startResp.Location()
	if err != nil {
		t.Fatalf("no redirect from handoff start: %v", err)
	}
	var hsCookie *http.Cookie
	for _, c := range startResp.Cookies() {
		if c.Name == "lotus_hs" {
			hsCookie = c
		}
	}

	authorizeReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, authorizeURL.RequestURI())
	authorizeReq.AddCookie(authCookie)
	authorizeResp, err := app.Test(authorizeReq, -1)
	if err != nil {
		t.Fatalf("handoff authorize: %v", err)
	}
	if authorizeResp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(authorizeResp.Body)
		t.Fatalf("handoff authorize status = %d, want 302 (creator should already be an active member): %s", authorizeResp.StatusCode, body)
	}
	finishURL, _ := authorizeResp.Location()

	finishReq := hostReq(http.MethodGet, handoffStartURL.Host, finishURL.RequestURI())
	finishReq.AddCookie(hsCookie)
	finishResp, err := app.Test(finishReq, -1)
	if err != nil {
		t.Fatalf("handoff finish: %v", err)
	}
	if finishResp.StatusCode != http.StatusFound {
		t.Fatalf("handoff finish status = %d, want 302", finishResp.StatusCode)
	}
}

func TestOnboarding_ConcurrentSameSlugOnlyOneWins(t *testing.T) {
	t.Parallel()
	app, db, cfg := newOnboardingTestApp(t)
	_, sessionA := seedOnboardingUser(t, db)
	_, sessionB := seedOnboardingUser(t, db)

	submit := func(sessionRaw string) int {
		req := hostFormReq("auth."+cfg.BaseDomain, "/onboarding/org", "name=Race&slug=race-slug")
		req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
		resp, err := app.Test(req, -1) // no timeout; both run concurrently
		if err != nil {
			t.Errorf("submit: %v", err)
			return 0
		}
		return resp.StatusCode
	}

	var wg sync.WaitGroup
	results := make([]int, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results[0] = submit(sessionA) }()
	go func() { defer wg.Done(); results[1] = submit(sessionB) }()
	wg.Wait()

	successes := 0
	for _, code := range results {
		if code == http.StatusFound {
			successes++
		} else if code != http.StatusUnprocessableEntity {
			t.Errorf("unexpected status %d (want 302 or 422, never 500)", code)
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}

	var orgCount int
	if err := db.Get(&orgCount, `SELECT count(*) FROM organizations WHERE subdomain = 'race-slug'`); err != nil {
		t.Fatalf("count orgs: %v", err)
	}
	if orgCount != 1 {
		t.Fatalf("orgs with slug 'race-slug' = %d, want 1", orgCount)
	}
}

func TestOnboarding_SlugRejectionCases(t *testing.T) {
	t.Parallel()
	app, db, cfg := newOnboardingTestApp(t)
	_, sessionRaw := seedOnboardingUser(t, db)

	cases := []struct {
		name string
		slug string
	}{
		{"reserved", "www"},
		{"double hyphen", "ac--me"},
		{"punycode prefix", "xn--foo"},
		{"too short", "ab"},
		{"leading hyphen", "-acme"},
	}
	for _, c := range cases {
		t.Run("check/"+c.name, func(t *testing.T) {
			req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/onboarding/slug-check?slug="+c.slug)
			req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "✗") {
				t.Fatalf("slug %q: expected rejection marker, got: %s", c.slug, body)
			}
		})
		t.Run("submit/"+c.name, func(t *testing.T) {
			req := hostFormReq("auth."+cfg.BaseDomain, "/onboarding/org", "name=Test&slug="+c.slug)
			req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if resp.StatusCode == http.StatusFound {
				t.Fatalf("slug %q: expected rejection, got 302 redirect (org created)", c.slug)
			}
		})
	}

	t.Run("retired slug rejected", func(t *testing.T) {
		var orgID uuid.UUID
		if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('retired-corp-tmp', 'x') RETURNING id`); err != nil {
			t.Fatalf("seed org: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO retired_slugs (slug, org_id) VALUES ('retired-corp', $1)`, orgID); err != nil {
			t.Fatalf("seed retired slug: %v", err)
		}
		req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/onboarding/slug-check?slug=retired-corp")
		req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "✗") {
			t.Fatalf("retired slug: expected rejection marker, got: %s", body)
		}
	})
}

func TestOnboarding_SlugCheckAcceptsAvailable(t *testing.T) {
	t.Parallel()
	app, db, cfg := newOnboardingTestApp(t)
	_, sessionRaw := seedOnboardingUser(t, db)
	req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/onboarding/slug-check?slug=brand-new-org")
	req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "✓") {
		t.Fatalf("expected availability marker, got: %s", body)
	}
}
