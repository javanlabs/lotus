package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQASignoff_GuestSignsOffAndThePlanShowsTheLatest(t *testing.T) {
	r := newQARunEnv(t, "acme-signoff")
	ctx := context.Background()
	planPath := r.base + "/plans/" + r.plan.ID.String()
	scenarios := mustPlanScenarios(t, r.qaEnv, r.plan.ID)
	scenario := scenarios[0].String()

	status, body := r.get(t, r.guest, planPath)
	if status != http.StatusOK || !strings.Contains(body, `action="`+planPath+`/signoffs"`) {
		t.Fatalf("guest plan page = %d, no sign-off form", status)
	}
	// Rejecting without saying why is refused, in the dialog.
	if _, body := r.post(t, r.guest, planPath+"/signoffs", url.Values{"scope": {scenario}, "decision": {"rejected"}}, true); !strings.Contains(body, "Say what is wrong") {
		t.Fatalf("rejected without a note:\n%s", body)
	}
	for _, form := range []url.Values{
		{"scope": {scenario}, "decision": {"rejected"}, "note": {"Invoice <total> is wrong"}},
		{"scope": {scenario}, "decision": {"accepted"}},
		{"scope": {"plan"}, "decision": {"accepted_with_notes"}, "note": {"Copy fixes pending"}},
	} {
		if resp, _ := r.post(t, r.guest, planPath+"/signoffs", form, false); resp.StatusCode != http.StatusFound {
			t.Fatalf("sign off %v = %d", form, resp.StatusCode)
		}
	}
	if n := auditCount(t, r.db, r.orgID, "qa.signoff"); n != 3 {
		t.Fatalf("audit qa.signoff = %d; want 3", n)
	}
	latest, _ := repo.ListLatestQASignoffs(ctx, r.s, r.plan.ID)
	if len(latest) != 2 || latest[1].Decision != "accepted" {
		t.Fatalf("latest = %+v", latest)
	}

	// The member reads the standing sign-offs (only the latest per scope)
	// but may not sign.
	status, body = r.get(t, r.member, planPath)
	if status != http.StatusOK || !strings.Contains(body, "Accepted with notes") || !strings.Contains(body, "Copy fixes pending") ||
		strings.Contains(body, "Invoice &lt;total&gt; is wrong") || !strings.Contains(body, "client@bank.example") {
		t.Fatalf("member plan page = %d; sign-offs missing or stale", status)
	}
	if strings.Contains(body, `action="`+planPath+`/signoffs"`) {
		t.Errorf("member sees the sign-off form")
	}
	if resp, _ := r.post(t, r.member, planPath+"/signoffs", url.Values{"scope": {"plan"}, "decision": {"accepted"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member sign-off = %d; want 403", resp.StatusCode)
	}
	// A scenario outside the plan, or of another project, is refused.
	other := r.project(t, "Mobile")
	foreign, _ := repo.CreateQAScenario(ctx, r.s, other, "Elsewhere", "uat", "", nil)
	if _, body := r.post(t, r.guest, planPath+"/signoffs", url.Values{"scope": {foreign.ID.String()}, "decision": {"accepted"}}, true); !strings.Contains(body, "Choose the plan or one of its scenarios") {
		t.Errorf("foreign scenario:\n%s", body)
	}
	// A guest of another project gets 404 on this plan.
	otherGuest, otherGuestID := r.session(t, "else@bank.example", "guest")
	_ = repo.AddProjectMember(ctx, r.s, other, otherGuestID)
	if resp, _ := r.post(t, otherGuest, planPath+"/signoffs", url.Values{"scope": {"plan"}, "decision": {"accepted"}}, false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("guest of another project = %d; want 404", resp.StatusCode)
	}
}
