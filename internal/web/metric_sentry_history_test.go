package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The Sentry page reads its history like the SonarQube one: a date
// picker over the days a snapshot was captured, and ?date= shows what was
// current at the end of that day.
func TestSentryDetail_DatePicker(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, userID := seedMember(t, db, "acme-sentry-hist", "owner")
	raw := seedSession(t, db, orgID, userID)
	host := "acme-sentry-hist." + dashboardTestDomain
	var projectID, componentID, connID, bindingID uuid.UUID
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		must := func(dst *uuid.UUID, q string, a ...any) {
			if err := tx.Get(dst, q, a...); err != nil {
				t.Fatal(err)
			}
		}
		must(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','Payments') RETURNING id`, orgID)
		must(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'api','api','backend') RETURNING id`, orgID, projectID)
		must(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'sentry','Sentry','https://sentry.example') RETURNING id`, orgID)
		must(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1,$2,$3,'sentry','backend') RETURNING id`, orgID, componentID, connID)
	})
	s := tenant.FromRun(db, orgID)
	for _, n := range []int{4, 11} {
		snap := repo.NewSentrySnapshot{ComponentID: componentID, ContentHash: "h" + string(rune('0'+n%10)), Environment: "production", ProjectSlug: "backend", TotalIssues: n}
		if err := repo.ReplaceSentrySnapshots(context.Background(), s, componentID, bindingID, collector.Headline{}, []repo.NewSentrySnapshot{snap}); err != nil {
			t.Fatal(err)
		}
	}
	page := "/p/" + projectID.String() + "/c/" + componentID.String() + "/m/" + bindingID.String()
	today := time.Now().Format("2006-01-02")

	status, body := getPage(t, app, host, page, raw)
	if status != http.StatusOK || !strings.Contains(body, `id="lotus-combobox-date"`) || !strings.Contains(body, `data-value="`+today+`"`) {
		t.Fatalf("sentry page = %d; no snapshot date picker", status)
	}
	if status, body := getPage(t, app, host, page+"?date="+today, raw); status != http.StatusOK || !strings.Contains(body, "as of "+today) {
		t.Errorf("sentry page ?date= = %d; no as-of note", status)
	}
}

func getPage(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, host, path, raw string) (int, string) {
	t.Helper()
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
