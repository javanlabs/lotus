package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// seedRepoScan is a component with a repository-scan Trivy binding on a
// GitLab connection, as the scheduler creates it, and no scan yet.
func seedRepoScan(t *testing.T, db *sqlx.DB, subdomain, role string) (host, raw string, bindingID uuid.UUID) {
	t.Helper()
	orgID, userID := seedMember(t, db, subdomain, role)
	raw = seedSession(t, db, orgID, userID)
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID, componentID, connID uuid.UUID
		must := func(dst *uuid.UUID, q string, args ...any) {
			if err := tx.Get(dst, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		must(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'AlurKerja') RETURNING id`, orgID)
		must(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'Camunda', 'backend') RETURNING id`, orgID, projectID)
		must(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'GitLab', 'https://gitlab.example.com') RETURNING id`, orgID)
		must(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config, sync_interval_seconds)
			VALUES ($1, $2, $3, 'trivy', 'alurkerja/camunda', '{"source":"repo"}', 86400) RETURNING id`, orgID, componentID, connID)
	})
	return subdomain + "." + dashboardTestDomain, raw, bindingID
}

func doScanReq(t *testing.T, req *http.Request, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// The Trivy page of a repository scan carries the scan's bar. Scan now
// queues a manual run and the bar turns to waiting and polls; once the
// scan has finished, the next poll tells htmx to reload the page.
func TestRepoScan_ScanNowQueuesAndThePageReloadsWhenDone(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	host, raw, bindingID := seedRepoScan(t, db, "acme-scan", "member")
	base := "/projects/p/components/c/metrics/trivy"

	resp, body := doScanReq(t, reqWithSession(http.MethodGet, host, base, raw), app)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `data-scan-state="idle"`) ||
		!strings.Contains(body, "Scan now") || !strings.Contains(body, "not been scanned yet") {
		t.Fatalf("page = %d, want the idle scan bar with Scan now:\n%s", resp.StatusCode, body)
	}

	resp, body = doScanReq(t, reqWithSession(http.MethodPost, host, base+"/scan", raw), app)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `data-scan-state="waiting"`) ||
		!strings.Contains(body, `hx-get="`+base+`/scan-status?was=active"`) {
		t.Fatalf("Scan now = %d, want the waiting bar, polling:\n%s", resp.StatusCode, body)
	}
	var trigger, status string
	if err := db.QueryRow(`SELECT trigger, status FROM sync_runs WHERE binding_id = $1`, bindingID).Scan(&trigger, &status); err != nil {
		t.Fatalf("queued run: %v", err)
	}
	if trigger != "manual" || status != "queued" {
		t.Fatalf("run = %s/%s, want a queued manual run", trigger, status)
	}

	// Pressing it again while one is queued adds nothing.
	doScanReq(t, reqWithSession(http.MethodPost, host, base+"/scan", raw), app)
	var n int
	if err := db.Get(&n, `SELECT count(*) FROM sync_runs WHERE binding_id = $1`, bindingID); err != nil || n != 1 {
		t.Fatalf("%d runs (%v), want still one", n, err)
	}

	if _, err := db.Exec(`UPDATE sync_runs SET status = 'running', started_at = now() WHERE binding_id = $1`, bindingID); err != nil {
		t.Fatal(err)
	}
	resp, body = doScanReq(t, reqWithSession(http.MethodGet, host, base+"/scan-status?was=active", raw), app)
	if !strings.Contains(body, `data-scan-state="scanning"`) || resp.Header.Get("HX-Refresh") != "" {
		t.Fatalf("while running: refresh %q, body:\n%s", resp.Header.Get("HX-Refresh"), body)
	}

	if _, err := db.Exec(`UPDATE sync_runs SET status = 'failed', finished_at = now(), error = 'trivy fs: exit status 1: db download failed' WHERE binding_id = $1`, bindingID); err != nil {
		t.Fatal(err)
	}
	resp, body = doScanReq(t, reqWithSession(http.MethodGet, host, base+"/scan-status?was=active", raw), app)
	if resp.Header.Get("HX-Refresh") != "true" || !strings.Contains(body, "Last scan failed") || !strings.Contains(body, "db download failed") {
		t.Fatalf("after it finished: refresh %q, body:\n%s", resp.Header.Get("HX-Refresh"), body)
	}
}

// A viewer sees the bar but cannot start a scan.
func TestRepoScan_ViewerCannotScan(t *testing.T) {
	t.Parallel()
	app, db := newDashboardTestApp(t)
	host, raw, _ := seedRepoScan(t, db, "acme-scan-viewer", "viewer")
	base := "/projects/p/components/c/metrics/trivy"

	_, body := doScanReq(t, reqWithSession(http.MethodGet, host, base, raw), app)
	if !strings.Contains(body, `id="trivy-scan"`) || strings.Contains(body, "Scan now") {
		t.Fatalf("viewer's page: want the bar without Scan now:\n%s", body)
	}
	if resp, _ := doScanReq(t, reqWithSession(http.MethodPost, host, base+"/scan", raw), app); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("viewer's Scan now = %d, want refused", resp.StatusCode)
	}
}
