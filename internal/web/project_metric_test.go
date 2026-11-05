package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

// kindPageFixture is a project "payments" with three components — two
// bound to the kind under test, one not — and a second project whose
// bound component must never show on the first's page.
type kindPageFixture struct {
	e                       qaEnv
	projectID               uuid.UUID
	billing, ledger, docs   uuid.UUID
	billingBind, ledgerBind uuid.UUID
	otherComp, otherBind    uuid.UUID
}

func seedKindPage(t *testing.T, sub, kind string) kindPageFixture {
	t.Helper()
	f := kindPageFixture{e: newQAEnv(t, sub)}
	provider := providerForKind(kind)
	withOrgTx(t, f.e.db, f.e.orgID, func(tx *sqlx.Tx) {
		must := func(dst *uuid.UUID, q string, a ...any) {
			t.Helper()
			if err := tx.Get(dst, q, a...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		var otherProject, conn uuid.UUID
		must(&f.projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'payments','Payments') RETURNING id`, f.e.orgID)
		must(&otherProject, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'other','Other') RETURNING id`, f.e.orgID)
		comp := func(dst *uuid.UUID, project uuid.UUID, slug, name string) {
			must(dst, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,$3,$4,'backend') RETURNING id`, f.e.orgID, project, slug, name)
		}
		comp(&f.billing, f.projectID, "billing", "billing-api")
		comp(&f.ledger, f.projectID, "ledger", "ledger-api")
		comp(&f.docs, f.projectID, "docs", "docs-site")
		comp(&f.otherComp, otherProject, "intruder", "intruder-api")
		must(&conn, `INSERT INTO integration_connections (org_id, provider, name, base_url, config) VALUES ($1,$2,$2,'https://tool.example','{"org":"acme"}') RETURNING id`, f.e.orgID, provider)
		bind := func(dst *uuid.UUID, component uuid.UUID, key string) {
			must(dst, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,$4,$5,'{}') RETURNING id`, f.e.orgID, component, conn, kind, key)
		}
		bind(&f.billingBind, f.billing, "grp/billing-api")
		bind(&f.ledgerBind, f.ledger, "grp/ledger-api")
		bind(&f.otherBind, f.otherComp, "grp/intruder-key")
		// billing scores 92 and ledger 60: the project's KPI card is their
		// average, 76.
		for _, r := range []struct {
			project, component uuid.UUID
			status             string
			score              int
		}{{f.projectID, f.billing, "pass", 92}, {f.projectID, f.ledger, "fail", 60}, {otherProject, f.otherComp, "pass", 85}} {
			if _, err := tx.Exec(`
				INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
				VALUES ($1, $2, $3, jsonb_build_object($4::text, jsonb_build_object('status', $5::text, 'grade', 'B', 'score', $6::int, 'updated_at', now())), $5, $6, now())`,
				f.e.orgID, r.project, r.component, kind, r.status, r.score); err != nil {
				t.Fatal(err)
			}
		}
	})
	return f
}

func (f kindPageFixture) exec(t *testing.T, q string, a ...any) {
	t.Helper()
	withOrgTx(t, f.e.db, f.e.orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	})
}

var headlineRe = regexp.MustCompile(`data-headline[^>]*>([^<]*)<`)

// checkKindPage asserts what every kind's page shares: the headline is the
// project page's KPI value, each bound component is a row opening its
// binding's detail page, the unbound one is in the footer, and nothing of
// the other project leaks in.
func (f kindPageFixture) checkKindPage(t *testing.T, kind string, wants ...string) string {
	t.Helper()
	status, body := f.e.get(t, f.e.owner, "/projects/payments/metrics/"+kind)
	if status != http.StatusOK {
		t.Fatalf("status %d:\n%s", status, body)
	}
	m := headlineRe.FindStringSubmatch(body)
	if m == nil || strings.TrimSpace(m[1]) != "76" {
		t.Fatalf("headline = %v, want the KPI card's 76", m)
	}
	// The same number the project page's own card shows.
	if kpi := f.projectKPI(t, kind); kpi.Value != "76" || kpi.Href != "/projects/payments/metrics/"+kind {
		t.Fatalf("project page KPI = %+v", kpi)
	}
	// The project's metric tabs are here too, this kind's open, and the
	// kind's other numbers sit directly under them.
	assertMetricTabs(t, body, "/projects/payments", kind)
	assertStatsBeneathTabs(t, body)
	project := &repo.Project{ID: f.projectID, Slug: "payments"}
	for _, row := range []struct {
		id   uuid.UUID
		slug string
	}{{f.billing, "billing"}, {f.ledger, "ledger"}} {
		href := metricURL(project, &repo.Component{ID: row.id, Slug: row.slug}, kind)
		if !strings.Contains(body, `href="`+href+`"`) {
			t.Errorf("no row linking to %s", href)
		}
	}
	if i := strings.Index(body, "data-unbound"); i < 0 || !strings.Contains(body[i:], "docs-site") ||
		!strings.Contains(body[i:], "1 component has no "+kindLabels[kind]+" binding") {
		t.Errorf("unbound footer missing docs-site")
	}
	if strings.Contains(body, "intruder") {
		t.Errorf("another project's component leaked onto the page")
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q", w)
		}
	}
	if t.Failed() {
		t.Logf("body:\n%s", body)
	}
	return body
}

// projectKPI is the project page's KPI card for kind, as its handler
// builds it.
func (f kindPageFixture) projectKPI(t *testing.T, kind string) kpiCard {
	t.Helper()
	comps, err := repo.ListComponents(context.Background(), f.e.s, f.projectID, false)
	if err != nil {
		t.Fatal(err)
	}
	infos := make([]componentInfo, 0, len(comps))
	for _, c := range comps {
		infos = append(infos, componentInfo{ID: c.ID, Name: c.Name, Slug: c.Slug, ProjectID: f.projectID, ProjectSlug: "payments"})
	}
	rows, err := repo.ListComponentMetricsForProject(context.Background(), f.e.s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	metrics := map[uuid.UUID]repo.ComponentMetric{}
	for _, r := range rows {
		metrics[r.ComponentID] = r
	}
	for _, k := range buildDashboardViewModel("", 1, infos, metrics).KPIs {
		if k.Key == kind {
			return k
		}
	}
	t.Fatalf("no KPI card for %s", kind)
	return kpiCard{}
}

func TestProjectMetricPage_Sonar(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-sonar", "sonarqube")
	ins := `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, coverage, bugs, code_smells, vulnerabilities, ncloc, duplicated_lines_density, reliability_rating)
	        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'A')`
	f.exec(t, ins, f.e.orgID, f.billingBind, f.billing, "OK", 94.5, 0, 12, 0, 12000, 1.5)
	f.exec(t, ins, f.e.orgID, f.ledgerBind, f.ledger, "ERROR", 61.0, 3, 140, 2, 8000, 31.0)
	f.exec(t, ins, f.e.orgID, f.otherBind, f.otherComp, "OK", 12.3, 0, 0, 0, 1, 0.0)
	// ledger's SonarQube last synced three days ago: its gate is drawn
	// stale, with the note saying so.
	f.exec(t, `UPDATE component_metrics_current
		SET metrics = jsonb_set(metrics, '{sonarqube,updated_at}', to_jsonb(now() - interval '3 days'))
		WHERE component_id = $1`, f.ledger)
	body := f.checkKindPage(t, "sonarqube",
		"SONARQUBE", "AVG COVERAGE", "77.8%", "20,000", // summary: average coverage, total LOC
		"94.5%", "61.0%", "passed", "failed",
		`href="https://tool.example/dashboard?id=grp%2Fbilling-api"`, `rel="noopener"`,
		// The project is named first, heading and tab title alike; the
		// heading names the kind by its logo.
		`Payments <span style="color:#C4B5CC;font-weight:400">·</span> <svg role="img" aria-label="SonarQube"`, "<title>Payments · SonarQube · Lotus</title>")
	if n := strings.Count(body, "lotus-badge-stale"); n != 1 || !strings.Contains(body, "SonarQube has not synced in more than 24 hours") {
		t.Errorf("%d stale cells, want ledger's gate alone", n)
	}
	if strings.Contains(body, "12.3%") {
		t.Error("another project's coverage leaked into the page")
	}
	// The legacy thresholds draw ledger's coverage, bugs, smells, dup and
	// vulnerabilities red — five cells — and nothing of billing's.
	if n := strings.Count(body, "lotus-pm-text lotus-text-fail"); n != 5 {
		t.Errorf("%d cells over threshold, want 5", n)
	}
}

// A component scanned by repository (no image) links out to its GitLab
// project, not to a Harbor path built on GitLab's address, and its row
// names the commit it scanned where an image row lists its tags.
func TestProjectMetricPage_TrivyRepositoryScan(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-trivy-repo", "trivy")
	f.exec(t, `UPDATE metric_bindings SET config = '{"source":"repo"}', external_key = 'grp/ledger-api' WHERE id = $1`, f.ledgerBind)
	ins := `INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, digest, tags, severity, total, critical, high, medium, low, fixable)
	        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`
	f.exec(t, ins, f.e.orgID, f.billingBind, f.billing, "registry", nil, "{v1.4.0}", "None", 0, 0, 0, 0, 0, 0)
	f.exec(t, ins, f.e.orgID, f.ledgerBind, f.ledger, "repo", "e7ef1359715d57e6ecf3a2503d691154d6bed1c4", "{}", "Critical", 9, 2, 3, 4, 0, 5)
	f.checkKindPage(t, "trivy", "commit e7ef1359", `href="https://tool.example/grp/ledger-api"`,
		`href="https://tool.example/harbor/projects/grp/repositories/billing-api"`)
}

func TestProjectMetricPage_Trivy(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-trivy", "trivy")
	ins := `INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, tags, severity, total, critical, high, medium, low, fixable, pulls)
	        VALUES ($1,$2,$3,'registry',$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	f.exec(t, ins, f.e.orgID, f.billingBind, f.billing, "{v1.4.0,latest}", "None", 0, 0, 0, 0, 0, 0, 40)
	f.exec(t, ins, f.e.orgID, f.ledgerBind, f.ledger, "{v2.0.1}", "Critical", 9, 2, 3, 4, 0, 5, 7)
	f.exec(t, `INSERT INTO trivy_finding_notes (org_id, binding_id, cve_id, package, status, comment) VALUES ($1,$2,'CVE-1','x','false_positive','n/a')`, f.e.orgID, f.ledgerBind)
	f.checkKindPage(t, "trivy", "TRIVY", "CRITICAL", "v1.4.0, latest", "clean", "critical",
		`href="https://tool.example/harbor/projects/grp/repositories/billing-api"`)
}

func TestProjectMetricPage_Sentry(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-sentry", "sentry")
	var prod uuid.UUID
	withOrgTx(t, f.e.db, f.e.orgID, func(tx *sqlx.Tx) {
		ins := `INSERT INTO sentry_snapshots (org_id, binding_id, component_id, environment, project_slug, sentry_project_id, sentry_org, status_filter, total_issues, score, grade)
		        VALUES ($1,$2,$3,$4,'web',42,'acme','is:unresolved',$5,$6,$7) RETURNING id`
		var ignored uuid.UUID
		if err := tx.Get(&prod, ins, f.e.orgID, f.ledgerBind, f.ledger, "production", 2, 40, "E"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Get(&ignored, ins, f.e.orgID, f.ledgerBind, f.ledger, "staging", 1, 90, "A"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO sentry_issues (org_id, snapshot_id, snapshot_captured_at, issue_id, substatus, event_count)
			SELECT org_id, id, captured_at, v.i, v.s, v.n FROM sentry_snapshots, (VALUES ('1','escalating',30),('2','new',7)) v(i,s,n) WHERE id = $1`, prod); err != nil {
			t.Fatal(err)
		}
	})
	// ledger's HEALTH cell is its headline score alone, no letter grade.
	body := f.checkKindPage(t, "sentry", "SENTRY", "OPEN ISSUES", `data-sort="60"`, ">37<",
		`href="https://tool.example/organizations/acme/issues/?project=42&amp;query=is%3Aunresolved&amp;statsPeriod=14d"`,
		// billing has no sync yet: its link falls back to the project by slug.
		`href="https://tool.example/organizations/acme/projects/grp%2Fbilling-api/"`)
	if strings.Contains(body, "B · ") || strings.Contains(body, "E · ") {
		t.Error("the HEALTH column still shows a letter grade")
	}
}

func TestProjectMetricPage_Gitlab(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-gitlab", "gitlab")
	ins := `INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, primary_language, default_branch, branches_total, branches_active, branches_stale,
	          last_pipeline_status, last_pipeline_web_url, pipeline_success_rate, pipeline_sample_size, commits_30d, commits_30d_capped, default_branch_protected, visibility, web_url)
	        VALUES ($1,$2,$3,'Go','main',12,3,2,$4,$5,0.9,20,$6,$7,$8,'private',$9)`
	f.exec(t, ins, f.e.orgID, f.billingBind, f.billing, "success", "https://git.example/p/1", 140, true, true, "https://git.example/grp/billing-api")
	f.exec(t, ins, f.e.orgID, f.ledgerBind, f.ledger, "failed", "https://git.example/p/2", 4, false, false, "https://git.example/grp/ledger-api")
	f.checkKindPage(t, "gitlab", "GITLAB", "FAILING PIPELINES", "PROTECTED DEFAULT", "1/2",
		"90% (18/20)", "100&#43;", "3 active · 2 stale", "unprotected",
		`href="https://git.example/p/1"`, `href="https://git.example/grp/ledger-api"`)
}

// A guest sees QA only: every other page answers as if it did not exist,
// listed on the project or not. An unknown kind is no page either.
func TestProjectMetricPage_GuestAndUnknownKindAre404(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-guest", "sonarqube")
	guest, guestID := f.e.session(t, "client@bank.example", "guest")
	if status, _ := f.e.get(t, guest, "/projects/payments/metrics/sonarqube"); status != http.StatusNotFound {
		t.Fatalf("unlisted guest = %d, want 404", status)
	}
	if err := repo.AddProjectMember(context.Background(), f.e.s, f.projectID, guestID); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.e.get(t, guest, "/projects/payments/metrics/sonarqube"); status != http.StatusNotFound {
		t.Fatalf("listed guest = %d, want 404", status)
	}
	if status, _ := f.e.get(t, f.e.owner, "/projects/payments/metrics/k6"); status != http.StatusNotFound {
		t.Fatalf("unknown kind = %d, want 404", status)
	}
	// Named by id, the page moves to its slug URL like the project page.
	resp, err := f.e.app.Test(reqWithSession(http.MethodGet, f.e.host, "/projects/"+f.projectID.String()+"/metrics/sonarqube", f.e.owner), -1)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/projects/payments/metrics/sonarqube" {
		t.Fatalf("by id = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// No component bound: an empty state pointing at where binding happens,
// not an empty table.
func TestProjectMetricPage_NothingBound(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-empty", "sonarqube")
	status, body := f.e.get(t, f.e.owner, "/projects/payments/metrics/sentry")
	if status != http.StatusOK || !strings.Contains(body, "No component has Sentry bound") || strings.Contains(body, "lotus-pm-row") {
		t.Fatalf("status %d:\n%s", status, body)
	}
	for _, name := range []string{"billing-api", "ledger-api", "docs-site"} {
		if !strings.Contains(body, name) {
			t.Errorf("empty state does not list %s to bind from", name)
		}
	}
}

// Each metric tab on a project page opens the project's page for its kind;
// the organization dashboard's cards (none are drawn there today) link
// nowhere, since there is no one project to open.
func TestProjectPage_MetricTabsOpenTheKindPages(t *testing.T) {
	t.Parallel()
	f := seedKindPage(t, "acme-pm-cards", "sonarqube")
	status, body := f.e.get(t, f.e.owner, "/projects/payments")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	// The score tab and one tab per kind.
	if n := strings.Count(body, `class="lotus-mt-link"`); n != len(bindableKinds)+1 {
		t.Errorf("%d metric tabs, want %d", n, len(bindableKinds)+1)
	}
	for _, kind := range bindableKinds {
		if !strings.Contains(body, `href="/projects/payments/metrics/`+kind+`" class="lotus-mt-link"`) {
			t.Errorf("no metric tab opens %s", kind)
		}
	}

	vm := buildProjectDashboardViewModel("", []repo.Project{{ID: f.projectID, Slug: "payments"}}, nil, nil, nil)
	for _, k := range vm.KPIs {
		if k.Href != "" {
			t.Errorf("organization KPI %s links to %q", k.Key, k.Href)
		}
	}
}
