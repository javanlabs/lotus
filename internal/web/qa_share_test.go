package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

var shareLink = regexp.MustCompile(`/s/qa/([0-9a-f]{64})`)

// openShared fetches a share link with no session at all.
func (r qaRunEnv) openShared(t *testing.T, host, token string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/s/qa/"+token, nil)
	req.Host = host
	resp, err := r.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestQAShare_LinkOpensTheReportUntilRevoked(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-share")
	ctx := context.Background()
	planPath := r.base + "/plans/" + r.plan.ID.String()
	if _, err := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{
		Title: "Total <b>rounds</b> wrong", Severity: "major", PlanID: &r.plan.ID, Source: "internal",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordQASignoff(ctx, r.s, r.plan.ID, repo.QASignoffInput{Decision: "rejected", Note: "Rounding"}, &r.guestID); err != nil {
		t.Fatal(err)
	}

	// Only qa.share creates links; the plan page offers it to a member.
	if _, body := r.get(t, r.member, planPath); !strings.Contains(body, `action="`+planPath+`/share"`) {
		t.Fatalf("member plan page has no share form")
	}
	if _, body := r.get(t, r.guest, planPath); strings.Contains(body, `action="`+planPath+`/share"`) {
		t.Errorf("guest sees the share form")
	}
	if resp, _ := r.post(t, r.guest, planPath+"/share", nil, false); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest share = %d; want 403", resp.StatusCode)
	}
	_, body := r.post(t, r.member, planPath+"/share", url.Values{}, true)
	m := shareLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("share response has no link:\n%s", body)
	}
	token := m[1]

	resp, body := r.openShared(t, r.host, token)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, r.plan.Name) || !strings.Contains(body, "Total &lt;b&gt;rounds&lt;/b&gt; wrong") ||
		!strings.Contains(body, "Rejected") || !strings.Contains(body, "Rounding") {
		t.Fatalf("shared report = %d:\n%s", resp.StatusCode, body)
	}
	// The signer is named, never shown by address: the guest here has no
	// name, and its email must not reach whoever holds the link.
	for _, leak := range []string{"<form", "/qa/files/", `href="/projects`, "hx-post", "client@bank.example"} {
		if strings.Contains(body, leak) {
			t.Errorf("shared report contains %q", leak)
		}
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "X-Robots-Tag": "noindex", "Referrer-Policy": "no-referrer"} {
		if got := resp.Header.Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q; want %q", header, got, want)
		}
	}
	// The token opens this report and nothing else.
	if status, _ := r.get(t, token, planPath); status == http.StatusOK {
		t.Errorf("token as a session opened the plan page")
	}
	if resp, _ := r.openShared(t, r.host, strings.Repeat("0", 64)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown token = %d; want 404", resp.StatusCode)
	}
	other := newQAEnv(t, "other-share")
	if resp, _ := r.openShared(t, other.host, token); resp.StatusCode != http.StatusNotFound {
		t.Errorf("token on another organization's host = %d; want 404", resp.StatusCode)
	}

	// The plan page lists the live link; revoking it ends it.
	links, _ := repo.ListQAShareLinks(ctx, r.s, r.plan.ID)
	if len(links) != 1 {
		t.Fatalf("links = %d", len(links))
	}
	revoke := planPath + "/share/" + links[0].ID.String() + "/revoke"
	if _, body := r.get(t, r.member, planPath); !strings.Contains(body, `action="`+revoke+`"`) {
		t.Errorf("plan page does not list the live link")
	}
	if resp, _ := r.post(t, r.member, revoke, nil, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	if resp, _ := r.openShared(t, r.host, token); resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoked link = %d; want 404", resp.StatusCode)
	}
	if resp, _ := r.post(t, r.member, revoke, nil, false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoke twice = %d; want 404", resp.StatusCode)
	}
	if a, b := auditCount(t, r.db, r.orgID, "qa.share.create"), auditCount(t, r.db, r.orgID, "qa.share.revoke"); a != 1 || b != 1 {
		t.Errorf("audit create/revoke = %d/%d; want 1/1", a, b)
	}

	// Signed in, anyone who reads QA prints the same report.
	status, body := r.get(t, r.guest, planPath+"/report")
	if status != http.StatusOK || !strings.Contains(body, "window.print()") || strings.Contains(body, "/qa/files/") {
		t.Fatalf("report for guest = %d", status)
	}
}
