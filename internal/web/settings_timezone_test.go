package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo/global"
)

// The organization's timezone is chosen from a list — UTC and Indonesia's
// three zones — not typed; anything else is refused and nothing changes.
func TestSettingsOrganization_TimezoneIsAChoice(t *testing.T) {
	app, db := newDashboardTestApp(t)
	orgID, ownerID := seedMember(t, db, "acme-tz", "owner")
	raw := seedSession(t, db, orgID, ownerID)
	host := "acme-tz." + dashboardTestDomain

	resp, err := app.Test(reqWithSession(http.MethodGet, host, "/settings/organization", raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, `id="lotus-combobox-timezone"`) || strings.Contains(page, `type="text" name="timezone"`) {
		t.Fatalf("timezone is not a dropdown")
	}
	for _, v := range []string{`data-value="UTC"`, `data-value="Asia/Jakarta"`, `data-value="Asia/Makassar"`, `data-value="Asia/Jayapura"`, "WIB", "WITA", "WIT"} {
		if !strings.Contains(page, v) {
			t.Errorf("timezone options lack %s", v)
		}
	}

	save := func(tz string) *http.Response {
		r := postForm(t, app, host, "/settings/organization", raw, url.Values{"name": {"Acme"}, "timezone": {tz}, "retention_days": {"90"}})
		r.Body.Close()
		return r
	}
	if r := save("Asia/Makassar"); r.StatusCode != http.StatusFound {
		t.Fatalf("save Asia/Makassar = %d", r.StatusCode)
	}
	if r := save("Europe/London"); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "error=") {
		t.Errorf("save Europe/London = %d %s; want refused", r.StatusCode, r.Header.Get("Location"))
	}
	p, _ := global.OrgProfileByID(t.Context(), db, orgID)
	if p.Timezone != "Asia/Makassar" {
		t.Errorf("timezone = %q; want Asia/Makassar kept", p.Timezone)
	}
}
