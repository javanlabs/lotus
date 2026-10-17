package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// seedMetricChromeFixture inserts a project/component/connection/binding
// chain (task 4.4's own metricChrome) directly, mirroring
// TestComponentPage_EnableDisableDeleteBinding's own pattern — the four
// detail pages only need this chain plus whichever kind's own snapshot
// table row a given test cares about.
func seedMetricChromeFixture(t *testing.T, db *sqlx.DB, orgID uuid.UUID, kind, externalKey string) (projectID, componentID, connID, bindingID string) {
	t.Helper()
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','billing-api','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		provider := kind
		if kind == "trivy" {
			provider = "harbor_trivy"
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,$2,$2,'https://example.com') RETURNING id`, orgID, provider); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config) VALUES ($1,$2,$3,$4,$5,'{}') RETURNING id`, orgID, componentID, connID, kind, externalKey); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return
}

func TestMetricDetailPage_CrossOrgBindingIs404NotAnotherOrgsData(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgA, userA := seedMember(t, db, "acme-metric-a", "owner")
	orgB, _ := seedMember(t, db, "acme-metric-b", "owner")
	raw := seedSession(t, db, orgA, userA)

	projectID, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgB, "gitlab", "grp/other-repo")

	host := "acme-metric-a." + dashboardTestDomain
	path := fmt.Sprintf("/p/%s/c/%s/m/%s", projectID, componentID, bindingID)
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404 (a binding from another org must 404, not 403 or 200):\n%s", resp.StatusCode, body)
	}
}

func TestMetricDetailPage_GitlabRendersFromSeededSnapshot(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-metric-gl", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgID, "gitlab", "grp/billing-api")

	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, primary_language, default_branch, branches_total, branches_active, branches_stale, last_pipeline_status, visibility, archived)
			VALUES ($1,$2,$3,'Go','main',10,3,7,'success','private',false)`,
			orgID, bindingID, componentID); err != nil {
			t.Fatalf("insert gitlab_snapshots: %v", err)
		}
	})

	host := "acme-metric-gl." + dashboardTestDomain
	path := metricPathOf(t, db, orgID, componentID, bindingID)
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Go", "main", "private", "snapshot captured", "last synced"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestSonarDetail_HistoricalDateShowsThatDatesSnapshotNotLatest(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-metric-sonar", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgID, "sonarqube", "grp/billing-api")

	oldDate := time.Now().AddDate(0, 0, -10)
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO sonar_snapshots (org_id, binding_id, component_id, captured_at, quality_gate, bugs, coverage)
			VALUES ($1,$2,$3,$4,'OK',1,77.7)`,
			orgID, bindingID, componentID, oldDate); err != nil {
			t.Fatalf("insert old sonar_snapshots: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, bugs, coverage)
			VALUES ($1,$2,$3,'ERROR',9,12.3)`,
			orgID, bindingID, componentID); err != nil {
			t.Fatalf("insert latest sonar_snapshots: %v", err)
		}
	})

	host := "acme-metric-sonar." + dashboardTestDomain
	base := metricPathOf(t, db, orgID, componentID, bindingID)

	latestResp, err := app.Test(reqWithSession(http.MethodGet, host, base, raw), -1)
	if err != nil {
		t.Fatalf("request (latest): %v", err)
	}
	defer latestResp.Body.Close()
	latestBody, _ := io.ReadAll(latestResp.Body)
	if !strings.Contains(string(latestBody), "12.3%") {
		t.Fatalf("latest view missing 12.3%% coverage (the newest snapshot):\n%s", latestBody)
	}

	dateParam := oldDate.Local().Format("2006-01-02")
	historicalResp, err := app.Test(reqWithSession(http.MethodGet, host, base+"?date="+dateParam, raw), -1)
	if err != nil {
		t.Fatalf("request (historical): %v", err)
	}
	defer historicalResp.Body.Close()
	historicalBody, _ := io.ReadAll(historicalResp.Body)
	if resp := historicalResp; resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, historicalBody)
	}
	if !strings.Contains(string(historicalBody), "77.7%") {
		t.Fatalf("?date=%s must show that date's snapshot (77.7%% coverage), not the latest:\n%s", dateParam, historicalBody)
	}
	if strings.Contains(string(historicalBody), "12.3%") {
		t.Fatalf("?date=%s leaked the latest snapshot's coverage (12.3%%) instead of the historical one:\n%s", dateParam, historicalBody)
	}
}

func TestTrivyDetail_RendersPerCVETableFromStoredBlobOnly(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-metric-trivy", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgID, "trivy", "library/billing-api")

	rawJSON := `{"severity":"Critical","vulnerabilities":[
		{"id":"CVE-2024-9999","package":"openssl","version":"1.1","fix_version":"1.2","severity":"Critical","description":"a stored finding","links":[],"cwe_ids":[]}
	]}`
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, total, critical, raw_json)
			VALUES ($1,$2,$3,'registry',1,1,$4)`,
			orgID, bindingID, componentID, rawJSON); err != nil {
			t.Fatalf("insert trivy_snapshots: %v", err)
		}
	})

	host := "acme-metric-trivy." + dashboardTestDomain
	path := metricPathOf(t, db, orgID, componentID, bindingID)
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	// The handler never wires a Harbor client into this route at all (see
	// registerMetricDetailRoutes / renderTrivyDetail) — a 200 built purely
	// from the seeded row already proves the page renders from the stored
	// blob, not a live call, since there is no live client to call.
	for _, want := range []string{"CVE-2024-9999", "openssl", "a stored finding"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestTrivyNotes_SaveThenDeleteRoundTrip(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-metric-trivynote", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgID, "trivy", "library/billing-api")

	rawJSON := `{"severity":"High","vulnerabilities":[
		{"id":"CVE-2024-8888","package":"libcurl","version":"7.0","fix_version":"","severity":"High","description":"d","links":[],"cwe_ids":[]}
	]}`
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, total, high, raw_json)
			VALUES ($1,$2,$3,'registry',1,1,$4)`,
			orgID, bindingID, componentID, rawJSON); err != nil {
			t.Fatalf("insert trivy_snapshots: %v", err)
		}
	})

	host := "acme-metric-trivynote." + dashboardTestDomain
	base := metricPathOf(t, db, orgID, componentID, bindingID)

	form := url.Values{"cve_id": {"CVE-2024-8888"}, "package": {"libcurl"}, "comment": {"not reachable from our entrypoint"}}
	saveReq := reqWithSession(http.MethodPost, host, base+"/trivy-notes", raw)
	saveReq.Body = io.NopCloser(strings.NewReader(form.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveReq.ContentLength = int64(len(form.Encode()))
	saveResp, err := app.Test(saveReq, -1)
	if err != nil {
		t.Fatalf("save request: %v", err)
	}
	defer saveResp.Body.Close()
	if saveResp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(saveResp.Body)
		t.Fatalf("save status = %d, want 302:\n%s", saveResp.StatusCode, b)
	}

	afterSaveResp, err := app.Test(reqWithSession(http.MethodGet, host, base, raw), -1)
	if err != nil {
		t.Fatalf("follow-up request: %v", err)
	}
	defer afterSaveResp.Body.Close()
	afterSaveBody, _ := io.ReadAll(afterSaveResp.Body)
	if !strings.Contains(string(afterSaveBody), "not reachable from our entrypoint") {
		t.Fatalf("body missing saved FP note after save:\n%s", afterSaveBody)
	}

	var noteID string
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&noteID, `SELECT id FROM trivy_finding_notes WHERE binding_id=$1 AND cve_id='CVE-2024-8888'`, bindingID); err != nil {
			t.Fatalf("read saved note: %v", err)
		}
	})

	deleteResp, err := app.Test(reqWithSession(http.MethodPost, host, base+"/trivy-notes/"+noteID+"/delete", raw), -1)
	if err != nil {
		t.Fatalf("delete request: %v", err)
	}
	defer deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(deleteResp.Body)
		t.Fatalf("delete status = %d, want 302:\n%s", deleteResp.StatusCode, b)
	}

	afterDeleteResp, err := app.Test(reqWithSession(http.MethodGet, host, base, raw), -1)
	if err != nil {
		t.Fatalf("follow-up request: %v", err)
	}
	defer afterDeleteResp.Body.Close()
	afterDeleteBody, _ := io.ReadAll(afterDeleteResp.Body)
	if strings.Contains(string(afterDeleteBody), "not reachable from our entrypoint") {
		t.Fatalf("body still shows the deleted FP note:\n%s", afterDeleteBody)
	}
}

func TestRunHistoryPage_ShowsStatusDurationErrorClassAndWarnings(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-metric-runs", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, connID, bindingID := seedMetricChromeFixture(t, db, orgID, "gitlab", "grp/billing-api")

	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status, started_at, finished_at, error_class, error, warnings_json)
			VALUES ($1,$2,$3,'gitlab','scheduled','failed', now() - interval '90 seconds', now(), 'upstream_5xx', 'gitlab returned 503', '["rate limited"]'::jsonb)`,
			orgID, connID, bindingID); err != nil {
			t.Fatalf("insert sync_runs: %v", err)
		}
	})

	host := "acme-metric-runs." + dashboardTestDomain
	path := metricPathOf(t, db, orgID, componentID, bindingID) + "/runs"
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"failed", "scheduled", "upstream_5xx", "gitlab returned 503", "rate limited"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}
