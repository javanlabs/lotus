package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/repo"
)

// statusPublishSection is the #status-publish partial within a page.
func statusPublishSection(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `id="status-publish"`)
	if at < 0 {
		t.Fatalf("no #status-publish in:\n%s", body)
	}
	end := strings.Index(body[at:], "</section>")
	if end < 0 {
		t.Fatalf("#status-publish is not closed in:\n%s", body)
	}
	return body[at : at+end]
}

func TestStatusPublish_OwnerTurnsItOnAndOff(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-status-owner")
	pid := e.project(t, "pay")

	_, body := e.get(t, e.owner, "/projects/pay")
	section := statusPublishSection(t, body)
	if strings.Contains(section, "/status/pay") {
		t.Fatalf("an unpublished project shows its public URL:\n%s", section)
	}
	if !strings.Contains(section, `name="enabled" value="1"`) {
		t.Fatalf("owner is not offered the switch:\n%s", section)
	}

	resp, _ := e.post(t, e.owner, "/projects/pay/status-page", url.Values{"enabled": {"1"}}, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/pay" {
		t.Fatalf("turn on = %d → %q, want 302 to /projects/pay", resp.StatusCode, resp.Header.Get("Location"))
	}
	p, err := repo.ProjectByID(context.Background(), e.s, pid)
	if err != nil || p.StatusPageEnabledAt == nil {
		t.Fatalf("after turning on: %+v, %v", p, err)
	}
	if n := auditCount(t, e.db, e.orgID, "project.status_page.enable"); n != 1 {
		t.Errorf("project.status_page.enable audit events = %d, want 1", n)
	}

	_, body = e.get(t, e.owner, "/projects/pay")
	section = statusPublishSection(t, body)
	// The organization's own origin, as share links are built (the test
	// app's config has no base domain).
	wantURL := config.Config{}.TenantURL("acme-status-owner") + "/status/pay"
	if !strings.Contains(section, wantURL) {
		t.Errorf("published project does not show %s:\n%s", wantURL, section)
	}
	if !strings.Contains(section, `data-testid="status-page-copy"`) {
		t.Errorf("no copy button:\n%s", section)
	}
	if !strings.Contains(section, `name="enabled" value="0"`) {
		t.Errorf("no way to turn it off:\n%s", section)
	}

	resp, _ = e.post(t, e.owner, "/projects/pay/status-page", url.Values{"enabled": {"0"}}, false)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("turn off = %d", resp.StatusCode)
	}
	if p, _ := repo.ProjectByID(context.Background(), e.s, pid); p.StatusPageEnabledAt != nil {
		t.Fatalf("after turning off: %v", p.StatusPageEnabledAt)
	}
	if n := auditCount(t, e.db, e.orgID, "project.status_page.disable"); n != 1 {
		t.Errorf("project.status_page.disable audit events = %d, want 1", n)
	}
}

// The UI hides the switch from everyone but owners and admins, and the
// route refuses on its own.
func TestStatusPublish_OnlyOwnersAndAdmins(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-status-member")
	pid := e.project(t, "pay")

	member, _ := e.session(t, "member@acme.example", "member")
	if resp, _ := e.post(t, member, "/projects/pay/status-page", url.Values{"enabled": {"1"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member post = %d, want 403", resp.StatusCode)
	}
	if p, _ := repo.ProjectByID(context.Background(), e.s, pid); p.StatusPageEnabledAt != nil {
		t.Fatalf("a member's post published the page")
	}

	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	status, body := e.get(t, viewer, "/projects/pay")
	if status != http.StatusOK {
		t.Fatalf("viewer project page = %d", status)
	}
	if strings.Contains(body, "/status-page") || strings.Contains(body, `id="status-publish"`) {
		t.Errorf("viewer is offered the status page switch")
	}

	admin, _ := e.session(t, "admin@acme.example", "admin")
	if resp, _ := e.post(t, admin, "/projects/pay/status-page", url.Values{"enabled": {"1"}}, false); resp.StatusCode != http.StatusFound {
		t.Errorf("admin post = %d, want 302", resp.StatusCode)
	}
}
