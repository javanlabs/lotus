package web

import (
	"github.com/uyab/lotus/internal/repo"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// seedChartFixture mirrors seedMetricChromeFixture (metric_detail_test.go,
// task 4.4) — a project/component/connection/binding chain — this file's
// own tests just need one, without task 4.4's file importing it as a
// shared helper (kept local so this file reads standalone).
func seedChartFixture(t *testing.T, db *sqlx.DB, orgID uuid.UUID, kind, externalKey string) (projectID, componentID, bindingID string) {
	t.Helper()
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var connID string
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

func seedMetricKeyAndFact(t *testing.T, db *sqlx.DB, orgID uuid.UUID, kind, componentID, bindingID, key, label, unit, direction string, active bool, value float64) {
	t.Helper()
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO metric_keys (org_id, kind, key, label, unit, value_type, direction, declared, active, first_seen_at, last_seen_at)
			VALUES ($1,$2,$3,$4,$5,'number',$6,false,$7, now() - interval '10 days', now() - interval '3 days')
			ON CONFLICT (org_id, kind, key) DO UPDATE SET label = EXCLUDED.label, active = EXCLUDED.active, last_seen_at = EXCLUDED.last_seen_at`,
			orgID, kind, key, label, unit, direction, active); err != nil {
			t.Fatalf("insert metric_keys: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
			VALUES ($1,$2,$3,$4,$5,$6, now() - interval '1 day')`,
			orgID, bindingID, componentID, key, value, unit); err != nil {
			t.Fatalf("insert metric_facts: %v", err)
		}
	})
}

func TestMetricCharts_NewMetricKeyAppearsWithoutTemplateChange(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-chart-newkey", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-chart-newkey." + dashboardTestDomain

	projectID, componentID, bindingID := seedChartFixture(t, db, orgID, "gitlab", "grp/billing-api")
	// A key no real collector declares — proves the chart grid is driven
	// entirely by the metric_keys catalog (task 3.7), not a hardcoded
	// per-kind field list: task 4.6's own acceptance criterion.
	seedMetricKeyAndFact(t, db, orgID, "gitlab", componentID, bindingID, "gitlab.made_up_metric", "Made Up Metric", "count", "higher_better", true, 42)

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/p/"+projectID+"/c/"+componentID+"/m/"+bindingID, raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Made Up Metric") {
		t.Fatalf("page missing the chart card for a metric_keys row no template hardcodes:\n%s", body)
	}
}

func TestMetricCharts_InactiveMetricStillViewableWithSinceDate(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-chart-inactive", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-chart-inactive." + dashboardTestDomain

	projectID, componentID, bindingID := seedChartFixture(t, db, orgID, "sonarqube", "grp/billing-api")
	seedMetricKeyAndFact(t, db, orgID, "sonarqube", componentID, bindingID, "sonar.coverage", "Coverage", "%", "higher_better", false, 88.5)

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/p/"+projectID+"/c/"+componentID+"/m/"+bindingID, raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Coverage", "not collected since"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("inactive metric card missing %q — an inactive key must still render, not disappear:\n%s", want, body)
		}
	}
}

func TestMetricCharts_RangeSwapReturnsAFragmentNotAFullPage(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-chart-range", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-chart-range." + dashboardTestDomain

	projectID, componentID, bindingID := seedChartFixture(t, db, orgID, "trivy", "library/billing-api")
	seedMetricKeyAndFact(t, db, orgID, "trivy", componentID, bindingID, "trivy.critical", "Critical", "count", "lower_better", true, 2)

	base := "/p/" + projectID + "/c/" + componentID + "/m/" + bindingID
	resp, err := app.Test(reqWithSession(http.MethodGet, host, base+"/charts?range=30d", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	bodyStr := string(body)
	if strings.Contains(bodyStr, "<html") || strings.Contains(bodyStr, "shell_open") {
		t.Fatalf("htmx range-swap endpoint returned a full page, not a fragment — a range change must swap in place, not reload:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Critical") {
		t.Fatalf("range fragment missing the chart card:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `hx-get="`+base+`/charts?range=30d"`) {
		t.Fatalf("30d button should carry its own hx-get for re-selection, and the fragment should reflect range=30d as selected:\n%s", bodyStr)
	}
}

func TestMetricCharts_CrossOrgBindingIs404(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgA, userA := seedMember(t, db, "acme-chart-cross-a", "owner")
	orgB, _ := seedMember(t, db, "acme-chart-cross-b", "owner")
	raw := seedSession(t, db, orgA, userA)

	projectID, componentID, bindingID := seedChartFixture(t, db, orgB, "gitlab", "grp/other")
	host := "acme-chart-cross-a." + dashboardTestDomain
	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/p/"+projectID+"/c/"+componentID+"/m/"+bindingID+"/charts", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for another org's binding", resp.StatusCode)
	}
}

func TestSparkline_RendersForBindingsPrimaryMetric(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-sparkline", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-sparkline." + dashboardTestDomain

	_, componentID, bindingID := seedChartFixture(t, db, orgID, "sentry", "billing-api")
	seedMetricKeyAndFact(t, db, orgID, "sentry", componentID, bindingID, "sentry.score", "Health score", "score", "higher_better", true, 88)

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/bindings/"+bindingID+"/sparkline", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d:\n%s", resp.StatusCode, body)
	}
	// The fragment is markup for sparklines.js, not a rendered chart: the
	// <svg> is built in the browser. What the server owes is the data
	// attributes the library reads, and points normalised into the 0-100
	// window it scales from zero.
	for _, want := range []string{`data-sparkline="true"`, `data-type="bar"`, "data-points=", "data-colors="} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("sparkline response missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), `data-points=""`) {
		t.Fatalf("sparkline rendered with no points:\n%s", body)
	}
}

func TestSparkline_NoDataRendersPlaceholderNotError(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-sparkline-empty", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-sparkline-empty." + dashboardTestDomain

	_, _, bindingID := seedChartFixture(t, db, orgID, "sentry", "billing-api")

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/bindings/"+bindingID+"/sparkline", raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with zero facts:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "no data") {
		t.Fatalf("empty sparkline should say \"no data\", got:\n%s", body)
	}
}

// A 90px bar sparkline cannot be handed a raw multi-day series.
// sparklines.js sizes each bar as (gap/n + width/n - gap), which turns
// NEGATIVE once n exceeds width+gap; the browser then rejects every
// <rect> with "attribute width: A negative value is not valid" and the
// chart renders blank. This pins the cap that prevents it.
func TestBuildChartSVG_SparkPointsAreCappedForTheBarRenderer(t *testing.T) {
	var points []repo.Point
	base := time.Now().Add(-7 * 24 * time.Hour)
	for i := 0; i < 300; i++ {
		points = append(points, repo.Point{
			At: base.Add(time.Duration(i) * time.Minute), HasValue: true, Value: float64(i % 50),
		})
	}
	got := buildChartSVG(points, "higher_better", "%", 90, 26)
	if !got.HasData {
		t.Fatal("HasData = false for 300 real points")
	}
	n := len(strings.Split(got.SparkPoints, ","))
	if n > sparkMaxBars {
		t.Errorf("SparkPoints has %d values, want at most %d — the bar renderer computes a negative width past that", n, sparkMaxBars)
	}
	// And the cap must not flatten the series into one repeated value.
	seen := map[string]bool{}
	for _, v := range strings.Split(got.SparkPoints, ",") {
		seen[v] = true
	}
	if len(seen) < 2 {
		t.Errorf("downsampling collapsed the series to %d distinct value(s): %q", len(seen), got.SparkPoints)
	}
}
