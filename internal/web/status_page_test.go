package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// openStatusPage fetches path on host as a browser with no session at all.
func openStatusPage(t *testing.T, e qaEnv, host, path string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	req.Header.Set("Accept", "text/html")
	resp, err := e.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func publishedProject(t *testing.T, s tenant.Scope, slug, name string) *repo.Project {
	t.Helper()
	ctx := context.Background()
	p, err := repo.CreateProject(ctx, s, repo.NewProject{Slug: slug, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetProjectStatusPage(ctx, s, p.ID, true); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStatusPage_Answers404Alike(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-status-404")
	ctx := context.Background()

	gone := publishedProject(t, e.s, "gone", "Gone")
	if err := repo.SoftDeleteProject(ctx, e.s, gone.ID); err != nil {
		t.Fatal(err)
	}
	off := publishedProject(t, e.s, "off", "Off")
	if err := repo.SetProjectStatusPage(ctx, e.s, off.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateProject(ctx, e.s, repo.NewProject{Slug: "never", Name: "Never"}); err != nil {
		t.Fatal(err)
	}
	otherOrg, _ := seedMember(t, e.db, "acme-status-other", "owner")
	publishedProject(t, tenant.FromRun(e.db, otherOrg), "theirs", "Theirs")

	var first string
	for _, slug := range []string{"nope", "gone", "off", "never", "theirs"} {
		resp, body := openStatusPage(t, e, e.host, "/status/"+slug)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/status/%s = %d, want 404", slug, resp.StatusCode)
			continue
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("/status/%s 404 body differs from /status/nope's", slug)
		}
	}
	// The other organization's own host does answer for it.
	if resp, _ := openStatusPage(t, e, "acme-status-other."+dashboardTestDomain, "/status/theirs"); resp.StatusCode != http.StatusOK {
		t.Errorf("/status/theirs on its own host = %d, want 200", resp.StatusCode)
	}
}

func TestStatusPage_PublishedRendersWithoutLogin(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-status-public")
	ctx := context.Background()
	p := publishedProject(t, e.s, "pay", "Payments")
	if _, err := repo.AddProjectTag(ctx, e.s, p.ID, "XLSMART"); err != nil {
		t.Fatal(err)
	}
	comp, err := repo.CreateComponent(ctx, e.s, repo.NewComponent{ProjectID: p.ID, Slug: "api", Name: "Billing API", Kind: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	withOrgTx(t, e.db, e.orgID, func(tx *sqlx.Tx) {
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url)
			VALUES ($1, 'sonarqube', 'sonar-internal-conn', 'https://sonar.example') RETURNING id`, e.orgID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'sonarqube', 'billing-api-key')`, e.orgID, comp.ID, connID); err != nil {
			t.Fatal(err)
		}
	})
	score := 41
	if err := repo.UpsertComponentMetric(ctx, e.s, comp.ID, "sonarqube", collector.Headline{
		Status: collector.StatusFail, Score: &score, Grade: "D",
		Summary: "Quality gate failed on secret-branch", DeepLink: "https://sonar.example/dashboard?id=billing",
	}); err != nil {
		t.Fatal(err)
	}

	resp, body := openStatusPage(t, e, e.host, "/status/pay")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	for _, want := range []string{"Payments", `data-testid="status-word">Failed<`, `data-testid="status-score">41<`,
		"Billing API", ">41<", "acme-status-public · Quality report", "Powered by Lotus"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	for _, leak := range []string{"deep_link", "sonar.example", "secret-branch", `href="/projects`, "sonar-internal-conn",
		"billing-api-key", "XLSMART", "lotus-sidebar"} {
		if strings.Contains(body, leak) {
			t.Errorf("body leaks %q", leak)
		}
	}
	// A garbage date falls back to today rather than failing.
	if resp, _ := openStatusPage(t, e, e.host, "/status/pay?date=%3Cscript%3E"); resp.StatusCode != http.StatusOK {
		t.Errorf("garbage date = %d, want 200", resp.StatusCode)
	}
	// A date before any recorded day reads No data.
	if _, body := openStatusPage(t, e, e.host, "/status/pay?date=2020-01-01"); !strings.Contains(body, `data-testid="status-word">No data<`) || strings.Contains(body, "Billing API") {
		t.Errorf("a day before the first record does not read No data")
	}
}

func TestStatusPage_ArchivedProjectStillAnswers(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-status-archived")
	p := publishedProject(t, e.s, "done", "Finished")
	if err := repo.SetProjectArchived(context.Background(), e.s, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if resp, body := openStatusPage(t, e, e.host, "/status/done"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Finished") {
		t.Fatalf("archived published project = %d", resp.StatusCode)
	}
}
