package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// The date picker lists calendar days as the database formats them, so
// picking one must mean the end of that same day — not the end of the
// day in the server process's zone. Here the process runs in WIB and the
// database in UTC: a snapshot at 20:00 UTC is listed under that UTC day,
// and picking that day must show it.
func TestSonarDetail_PickedDayIsTheListedDay(t *testing.T) {
	wib := time.FixedZone("WIB", 7*3600)
	prev := time.Local
	time.Local = wib
	t.Cleanup(func() { time.Local = prev })

	app, db := newDashboardTestApp(t)
	var sessionTZ string
	if err := db.Get(&sessionTZ, `SHOW timezone`); err != nil || sessionTZ != "UTC" {
		t.Skipf("needs a UTC test database; session timezone = %q (%v)", sessionTZ, err)
	}
	orgID, userID := seedMember(t, db, "acme-date-cutoff", "owner")
	raw := seedSession(t, db, orgID, userID)
	_, componentID, _, bindingID := seedMetricChromeFixture(t, db, orgID, "sonarqube", "grp/cutoff")
	d := time.Now().UTC().AddDate(0, 0, -10)
	at := time.Date(d.Year(), d.Month(), d.Day(), 20, 0, 0, 0, time.UTC) // already the next day in WIB
	day := at.Format("2006-01-02")
	withOrgTx(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`INSERT INTO sonar_snapshots (org_id, binding_id, component_id, captured_at, quality_gate, bugs, coverage)
			VALUES ($1,$2,$3,$4,'OK',1,77.7)`, orgID, bindingID, componentID, at); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, bugs, coverage)
			VALUES ($1,$2,$3,'ERROR',9,12.3)`, orgID, bindingID, componentID); err != nil {
			t.Fatal(err)
		}
	})
	host := "acme-date-cutoff." + dashboardTestDomain
	path := metricPathOf(t, db, orgID, componentID, bindingID) + "?date=" + day
	resp, err := app.Test(reqWithSession(http.MethodGet, host, path, raw), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `data-value="`+day+`"`) {
		t.Fatalf("status %d; %s not among the listed days", resp.StatusCode, day)
	}
	if !strings.Contains(string(body), "77.7%") {
		t.Errorf("?date=%s does not show the snapshot listed under that day (77.7%%)", day)
	}
}
