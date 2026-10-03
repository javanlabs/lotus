package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQACase_CreateAndEditBumpsVersionOnlyOnContent(t *testing.T) {
	e := newQAEnv(t, "acme-qacase")
	ctx := context.Background()
	member, _ := e.session(t, "qa@acme.example", "member")
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	base := e.projectPath(t, p) + "/qa/cases"

	status, body := e.get(t, member, base+"/new?section="+sec.ID.String())
	if status != http.StatusOK || !strings.Contains(body, `name="title"`) {
		t.Fatalf("new form = %d", status)
	}
	form := url.Values{
		"title": {"Login valid"}, "section_id": {sec.ID.String()}, "priority": {"high"},
		"precondition": {"User exists"}, "notes": {"https://taiga.javan.id/x"},
		"step_action":   {"Open https://portal", "", "Submit valid credentials"},
		"step_expected": {"Login page shows", "", "Dashboard opens"},
	}
	resp, _ := e.post(t, member, base+"/new", form, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != base+"/1" {
		t.Fatalf("create = %d %s; want 302 to %s/1", resp.StatusCode, resp.Header.Get("Location"), base)
	}
	c, err := repo.GetQACaseByKey(ctx, e.s, p, 1)
	if err != nil || c.Title != "Login valid" || c.Priority == nil || *c.Priority != "high" || len(c.Steps) != 2 ||
		c.Steps[1].Expected != "Dashboard opens" || c.Version != 1 {
		t.Fatalf("created case = %+v, %v (a blank step pair is dropped)", c, err)
	}

	// Notes are not test content: no new version.
	form.Set("notes", "moved to Jira")
	e.post(t, member, base+"/1", form, false)
	if c, _ = repo.GetQACaseByKey(ctx, e.s, p, 1); c.Version != 1 || c.Notes != "moved to Jira" {
		t.Fatalf("notes edit = version %d, notes %q", c.Version, c.Notes)
	}
	form["step_expected"] = []string{"Login page shows", "", "Home opens"}
	resp, _ = e.post(t, member, base+"/1", form, false)
	if c, _ = repo.GetQACaseByKey(ctx, e.s, p, 1); resp.StatusCode != http.StatusFound || c.Version != 2 {
		t.Fatalf("step edit = %d, version %d; want 302 and version 2", resp.StatusCode, c.Version)
	}

	status, body = e.get(t, member, base+"/1")
	if status != http.StatusOK || !strings.Contains(body, "TC-1") || !strings.Contains(body, "Version 2") {
		t.Fatalf("case page = %d", status)
	}
}

func TestQACase_RejectsBadInputAndEscapes(t *testing.T) {
	e := newQAEnv(t, "acme-qacase-bad")
	ctx := context.Background()
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	foreign, _ := repo.CreateQASection(ctx, e.s, other, "Invoices", nil)
	base := e.projectPath(t, p) + "/qa/cases"

	resp, body := e.post(t, e.owner, base+"/new", url.Values{"title": {strings.Repeat("x", 501)}, "section_id": {sec.ID.String()}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Title is required and can be at most 500 characters.") {
		t.Fatalf("long title = %d", resp.StatusCode)
	}
	resp, body = e.post(t, e.owner, base+"/new", url.Values{"title": {"X"}, "section_id": {sec.ID.String()}, "priority": {"urgent"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose a priority: high, medium or low.") {
		t.Fatalf("bad priority = %d", resp.StatusCode)
	}
	resp, _ = e.post(t, e.owner, base+"/new", url.Values{"title": {"X"}, "section_id": {foreign.ID.String()}}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("section of another project = %d; want 404", resp.StatusCode)
	}

	xss := `<script>alert(1)</script>`
	resp, _ = e.post(t, e.owner, base+"/new", url.Values{"title": {"Login " + xss}, "section_id": {sec.ID.String()},
		"step_action": {"Type " + xss}, "step_expected": {"</script><b>nothing</b>"}}, false)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("create with markup = %d", resp.StatusCode)
	}
	c, _ := repo.GetQACaseByKey(ctx, e.s, p, 1)
	if c.Steps[0].Action != "Type "+xss || c.Steps[0].Expected != "</script><b>nothing</b>" {
		t.Fatalf("steps not stored verbatim: %+v", c.Steps)
	}
	_, body = e.get(t, e.owner, base+"/1")
	if strings.Contains(body, xss) || strings.Contains(body, "<b>nothing</b>") {
		t.Fatal("case markup rendered unescaped")
	}
	if status, _ := e.get(t, e.owner, base+"/99"); status != http.StatusNotFound {
		t.Fatalf("unknown key = %d; want 404", status)
	}
}

func TestQACase_ViewerReadsGuestReadsArchiveIsAudited(t *testing.T) {
	e := newQAEnv(t, "acme-qacase-perm")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: "Login valid"}, nil)
	base := e.projectPath(t, p) + "/qa/cases"

	status, body := e.get(t, viewer, base+"/1")
	if status != http.StatusOK || !strings.Contains(body, "Login valid") || strings.Contains(body, `name="title"`) {
		t.Fatalf("viewer page = %d; want a read-only view", status)
	}
	if resp, _ := e.post(t, viewer, base+"/1", url.Values{"title": {"Hijack"}, "section_id": {sec.ID.String()}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer save = %d; want 403", resp.StatusCode)
	}
	if status, _ := e.get(t, viewer, base+"/new"); status != http.StatusForbidden {
		t.Fatalf("viewer new = %d; want 403", status)
	}

	resp, _ := e.post(t, e.owner, base+"/1/archive", nil, false)
	c, _ := repo.GetQACaseByKey(ctx, e.s, p, 1)
	if resp.StatusCode != http.StatusFound || c.ArchivedAt == nil || auditCount(t, e.db, e.orgID, "qa.case.archive") != 1 {
		t.Fatalf("archive = %d, archived %v", resp.StatusCode, c.ArchivedAt)
	}
	e.post(t, e.owner, base+"/1/restore", nil, false)
	if c, _ = repo.GetQACaseByKey(ctx, e.s, p, 1); c.ArchivedAt != nil {
		t.Fatal("restore did not restore")
	}
}
