package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// projectTagsSection is the #project-tags partial within a page or response
// (it holds no inner <div>).
func projectTagsSection(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `id="project-tags"`)
	if at < 0 {
		t.Fatalf("no #project-tags in:\n%s", body)
	}
	end := strings.Index(body[at:], "</div>")
	if end < 0 {
		t.Fatalf("#project-tags is not closed in:\n%s", body)
	}
	return body[at : at+end]
}

func TestProjectTags_OwnerAddsAndRemoves(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-tag-owner")
	pid := e.project(t, "pay")

	resp, body := e.post(t, e.owner, "/projects/pay/tags", url.Values{"name": {"xlsmart"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(projectTagsSection(t, body), "xlsmart") {
		t.Fatalf("add = %d:\n%s", resp.StatusCode, body)
	}
	// The header's read-only pills ride along out of band, so they follow.
	if !strings.Contains(body, `id="project-tag-pills"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("add response does not refresh the header's pills:\n%s", body)
	}
	tags, err := repo.ProjectTagsByProject(context.Background(), e.s)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags[pid]) != 1 {
		t.Fatalf("project tags = %v, want xlsmart", tags[pid])
	}

	resp, body = e.post(t, e.owner, "/projects/pay/tags/"+tags[pid][0].ID.String()+"/remove", nil, true)
	if resp.StatusCode != http.StatusOK || strings.Contains(projectTagsSection(t, body), "xlsmart") {
		t.Fatalf("remove = %d:\n%s", resp.StatusCode, body)
	}
	if n := auditCount(t, e.db, e.orgID, "project.tag.add"); n != 1 {
		t.Errorf("project.tag.add audit events = %d, want 1", n)
	}
	if n := auditCount(t, e.db, e.orgID, "project.tag.remove"); n != 1 {
		t.Errorf("project.tag.remove audit events = %d, want 1", n)
	}

	// Without htmx the add lands back on the project page.
	resp, _ = e.post(t, e.owner, "/projects/pay/tags", url.Values{"name": {"PROJECT"}}, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/pay" {
		t.Fatalf("plain add = %d → %q, want 302 to /projects/pay", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The UI hides the controls from a member, but the routes must refuse on
// their own.
func TestProjectTags_MemberIsForbidden(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-tag-member")
	pid := e.project(t, "pay")
	tag, err := repo.AddProjectTag(context.Background(), e.s, pid, "PROJECT")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := e.session(t, "member@acme.example", "member")
	if resp, _ := e.post(t, member, "/projects/pay/tags", url.Values{"name": {"xlsmart"}}, true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member add = %d, want 403", resp.StatusCode)
	}
	if resp, _ := e.post(t, member, "/projects/pay/tags/"+tag.ID.String()+"/remove", nil, true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member remove = %d, want 403", resp.StatusCode)
	}

	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	status, body := e.get(t, viewer, "/projects/pay")
	if status != http.StatusOK {
		t.Fatalf("viewer project page = %d", status)
	}
	if !strings.Contains(body, `<div id="project-tag-pills" class="lotus-tag-pills" data-testid="project-tag-pills"><span class="lotus-tag">PROJECT</span></div>`) {
		t.Errorf("viewer does not see the project's tag:\n%s", body)
	}
	if strings.Contains(body, `id="project-tags"`) || strings.Contains(body, "/tags") || strings.Contains(body, `data-testid="project-settings"`) {
		t.Errorf("viewer is offered tag controls or the settings button")
	}

	// The owner sees them, in the settings popover.
	_, body = e.get(t, e.owner, "/projects/pay")
	if !strings.Contains(body, `data-testid="project-settings-toggle"`) {
		t.Errorf("owner has no settings button")
	}
	section := projectTagsSection(t, body)
	if !strings.Contains(section, `name="name"`) || !strings.Contains(section, "/remove") {
		t.Errorf("owner is not offered tag controls:\n%s", section)
	}
}

func TestProjectTags_InvalidNameIs422(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-tag-invalid")
	e.project(t, "pay")
	resp, body := e.post(t, e.owner, "/projects/pay/tags", url.Values{"name": {"  "}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(projectTagsSection(t, body), "1–40 characters") {
		t.Fatalf("blank name = %d:\n%s", resp.StatusCode, body)
	}
}
