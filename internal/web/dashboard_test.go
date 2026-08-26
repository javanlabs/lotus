package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/tenant"
)

const dashboardTestDomain = "lotus.test"

// dashboardTestKEK, like internal/web/connections_test.go's own, is a
// fixed 32-byte test-only key — task 4.3's bind wizard creates real
// connections (envelope-encrypted, task 2.2), so it needs one too.
func dashboardTestKEK(t *testing.T) *crypto.KEK {
	t.Helper()
	kek, err := crypto.LoadKEK("bG90dXMtdGVzdC1rZWstMzItYnl0ZXMtZXhhY3RseVg=", 1)
	if err != nil {
		t.Fatalf("load test KEK: %v", err)
	}
	return kek
}

// dashboardFakeDNS mirrors connections_test.go's fakePublicDNS — the
// bind wizard's new-connection flow runs netguard.ValidateBaseURL, which
// resolves DNS for real unless overridden; tests must not depend on
// outbound network access.
func dashboardFakeDNS(_ context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if host == "localhost" {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	return []net.IP{net.ParseIP("203.0.113.10")}, nil
}

func newDashboardTestApp(t *testing.T) (*fiber.App, *sqlx.DB) {
	t.Helper()
	renderer := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	db := dbtest.Open(t)
	resolver := tenant.NewResolver(config.Config{BaseDomain: dashboardTestDomain, Dev: true}, db)
	app := New(Deps{
		Renderer:         renderer,
		StaticFS:         os.DirFS("../../web/static"),
		Resolver:         resolver,
		DB:               db,
		OAuthProviders:   authn.Registry{},
		KEK:              dashboardTestKEK(t),
		NetguardResolver: dashboardFakeDNS,
	})
	return app, db
}

func TestDashboard_NoSessionRedirectsRatherThanShowingData(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, _ := seedMember(t, db, "acme-nosession", "owner")
	_ = orgID
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Host = "acme-nosession." + dashboardTestDomain
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("status = 200 with no session, want an auth failure status")
	}
}

func TestDashboard_EmptyOrgShowsEmptyState(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-empty", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-empty." + dashboardTestDomain

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "No projects yet") {
		t.Fatalf("body missing empty-state copy:\n%s", body)
	}
}

// TestDashboard_ShowsProjectRowNotComponentRow pins the level the org
// dashboard aggregates at. A project is the outermost unit of the product
// and owns the Lotus score; components are what you find after drilling
// into one. Listing components here would skip the project level entirely,
// which is what this used to do.
func TestDashboard_ShowsProjectRowNotComponentRow(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-full", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-full." + dashboardTestDomain

	var projectID, componentID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
			VALUES ($1,$2,$3,'{"gitlab":{"status":"pass","score":77}}'::jsonb,'pass',77,now())`,
			orgID, projectID, componentID); err != nil {
			t.Fatalf("insert component metric: %v", err)
		}
	})

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/projects", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Payments", "77", "Software health"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	// The component's own name belongs on the project page, not here.
	if strings.Contains(string(body), "billing-api") {
		t.Fatalf("dashboard names the component \"billing-api\"; it should list projects only:\n%s", body)
	}
}

func TestNewProject_SubmitCreatesAndRedirects(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-newproj", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-newproj." + dashboardTestDomain

	form := url.Values{"name": {"Billing"}, "slug": {"billing"}}
	req := httptest.NewRequest(http.MethodPost, "http://"+host+"/projects/new", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 302 redirect:\n%s", resp.StatusCode, body)
	}
	// Redirects to the new project's own page (task 4.3), not the
	// org-wide dashboard — there's now somewhere more specific to land.
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/projects/") {
		t.Fatalf("Location = %q, want a /projects/<id> redirect", loc)
	}

	var count int
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&count, `SELECT count(*) FROM projects WHERE org_id = $1 AND slug = 'billing'`, orgID); err != nil {
			t.Fatalf("count: %v", err)
		}
	})
	if count != 1 {
		t.Fatalf("projects with slug=billing = %d, want 1", count)
	}
}

// TestDashboardPathStillForwards guards the compatibility redirect left
// behind when the landing page moved from /dashboard to /projects: links
// and bookmarks made before the move must keep working, and a silent 404
// there would look like the app losing a page.
func TestDashboardPathStillForwards(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-fwd", "owner")
	raw := seedSession(t, db, orgID, userID)

	resp, err := app.Test(reqWithSession(http.MethodGet, "acme-fwd."+dashboardTestDomain, "/dashboard", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/projects" {
		t.Fatalf("Location = %q, want /projects", got)
	}
}
