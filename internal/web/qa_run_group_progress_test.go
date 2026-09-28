package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Recording a result refreshes the count in its group's header too, not
// only the run's progress: the header said "0/2 done" until a reload.
func TestQARun_RecordRefreshesTheGroupHeader(t *testing.T) {
	r := newQARunEnv(t, "acme-group-progress")
	run, items := r.firstRun(t)

	status, page := r.get(t, r.member, r.base+"/runs/"+run.ID.String())
	if status != http.StatusOK || !strings.Contains(page, `id="group-progress-0"`) {
		t.Fatalf("run page = %d; the group header has no id to refresh", status)
	}
	resp, body := r.record(t, r.member, run.ID, items[0].ID, "passed", "")
	oob := regexp.MustCompile(`(?s)<span id="group-progress-0" hx-swap-oob="true"[^>]*>.*?</span>\s*</span>|<span id="group-progress-0" hx-swap-oob="true"`).FindString(body)
	if resp.StatusCode != http.StatusOK || oob == "" || !strings.Contains(body[strings.Index(body, `id="group-progress-0"`):], "1/2 done") {
		t.Fatalf("record = %d; no refreshed group header in:\n%s", resp.StatusCode, body)
	}
}
