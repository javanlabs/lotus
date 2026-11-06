package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// The dashboard against a real organization: ledger-api's SonarQube fails
// and keeps failing to sync, its connection cannot sign in, and a blocker
// is open and was moved along — each shows where the team will look.
func TestHomeDashboard_Page(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-home", "sonarqube")
	f.exec(t, `UPDATE metric_bindings SET consecutive_failures = 4 WHERE id = $1`, f.ledgerBind)
	f.exec(t, `UPDATE integration_connections SET status = 'auth_failed', last_error = 'bad token'`)
	ctx := context.Background()
	is, err := repo.CreateQAIssue(ctx, f.e.s, f.projectID, repo.QAIssueInput{Title: "Checkout crashes", Severity: "blocker"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ChangeQAIssueStatus(ctx, f.e.s, is.ID, "open", nil, ""); err != nil {
		t.Fatal(err)
	}

	status, body := f.e.get(t, f.e.owner, "/dashboard")
	if status != http.StatusOK {
		t.Fatalf("status %d:\n%s", status, body)
	}
	for _, want := range []string{
		`href="/projects/payments/components/ledger/metrics/sonarqube"`,
		"SonarQube sync keeps failing on ledger-api", `href="/projects/payments/components/ledger/metrics/sonarqube/runs"`,
		"cannot sign in", "bad token",
		"Blocker issue #1: Checkout crashes", `href="/projects/payments/qa/issues/1"`,
		"marked open", // the feed
		"SONARQUBE",   // the per-integration averages
		`data-testid="health"`, `data-testid="insights"`, `data-testid="health-map"`, `data-testid="trends"`,
		"SonarQube causes the most failures",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// The card that repeated the health map is gone; what the map cannot show
	// is listed where to start, and no component is reported twice.
	// ("Needs attention" itself is now the map legend's band word, so the
	// old card is recognised by its sentence.)
	if strings.Contains(body, "ledger-api is failing") {
		t.Error("the dashboard still repeats failing components outside the map")
	}
	if !strings.Contains(body[strings.Index(body, `data-testid="insights"`):], `data-testid="alerts"`) {
		t.Error("alerts are not inside Where to start")
	}
	// Dashboard is the sidebar's first item, and active here; the
	// breadcrumb's logo leads here.
	dash := strings.Index(body, `<a href="/dashboard" class="lotus-nav-item is-active">`)
	projects := strings.Index(body, `<a href="/projects" class="lotus-nav-item`)
	if dash < 0 || projects < 0 || dash > projects {
		t.Errorf("Dashboard is not the active sidebar item above Projects (%d, %d)", dash, projects)
	}
	if !strings.Contains(body, `<a href="/dashboard" aria-label="Lotus"`) {
		t.Error("breadcrumb logo does not lead to the dashboard")
	}
}

// Sign-in returns to /, which lands on the dashboard on an organization's
// host; a guest is sent on to its own work.
func TestHomeDashboard_LandingAndGuests(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-home-land")
	if status, loc, _ := e.get3(t, e.owner, "/"); status != http.StatusFound || loc != "/dashboard" {
		t.Fatalf("GET / = %d → %q; want 302 → /dashboard", status, loc)
	}
	guest, _ := e.session(t, "client@bank.example", "guest")
	if status, loc, _ := e.get3(t, guest, "/dashboard"); status != http.StatusFound || loc != "/qa/my" {
		t.Fatalf("guest GET /dashboard = %d → %q; want 302 → /qa/my", status, loc)
	}
	// An organization with nothing yet still has a page, saying so.
	status, body := e.get(t, e.owner, "/dashboard")
	if status != http.StatusOK || strings.Contains(body, `data-testid="alerts"`) || !strings.Contains(body, "No issue activity yet.") {
		t.Fatalf("empty dashboard = %d", status)
	}
}
