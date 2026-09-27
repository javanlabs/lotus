package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func qaSectionNamed(t *testing.T, e qaEnv, projectID uuid.UUID, name string) repo.QASection {
	t.Helper()
	secs, _ := repo.ListQASections(context.Background(), e.s, projectID)
	for _, s := range secs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no section %q in %+v", name, secs)
	return repo.QASection{}
}

func TestQASections_CreateRenameMoveArchive(t *testing.T) {
	e := newQAEnv(t, "acme-qasec")
	member, _ := e.session(t, "qa@acme.example", "member")
	p := e.project(t, "Portal")
	base := "/projects/" + p.String() + "/qa"

	resp, _ := e.post(t, member, base+"/sections", url.Values{"name": {"Auth"}}, false)
	auth := qaSectionNamed(t, e, p, "Auth")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != base+"/cases?section="+auth.ID.String() {
		t.Fatalf("create = %d %s; want 302 to the new section", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.post(t, member, base+"/sections", url.Values{"name": {"Login"}, "parent_id": {auth.ID.String()}}, true)
	login := qaSectionNamed(t, e, p, "Login")
	// From the dialog, htmx is sent on to the new section, not left where it was.
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != base+"/cases?section="+login.ID.String() {
		t.Fatalf("htmx create = %d, HX-Redirect %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	if login.ParentID == nil || *login.ParentID != auth.ID {
		t.Fatal("Login not nested under Auth")
	}

	// Rejections come back inside the dialog, escaped.
	resp, body := e.post(t, member, base+"/sections", url.Values{"name": {"auth"}}, true)
	if resp.StatusCode != http.StatusOK || !hasText(body, "There's already a section called 'auth' here.") {
		t.Fatalf("duplicate = %d %s", resp.StatusCode, body)
	}
	resp, body = e.post(t, member, base+"/sections/"+auth.ID.String()+"/move", url.Values{"parent_id": {login.ID.String()}}, true)
	if !hasText(body, "Can't move a section into itself or its own subsection.") {
		t.Fatalf("cycle = %d %s", resp.StatusCode, body)
	}
	resp, body = e.post(t, member, base+"/sections/"+login.ID.String()+"/rename", url.Values{"name": {"<b>Sign in</b>"}}, true)
	if resp.StatusCode != http.StatusNoContent || qaSectionNamed(t, e, p, "<b>Sign in</b>").ID != login.ID {
		t.Fatalf("rename = %d %s", resp.StatusCode, body)
	}
	resp, _ = e.post(t, member, base+"/sections/"+login.ID.String()+"/move", url.Values{"parent_id": {""}}, true)
	if resp.StatusCode != http.StatusNoContent || qaSectionNamed(t, e, p, "<b>Sign in</b>").ParentID != nil {
		t.Fatal("move to top level failed")
	}

	c, _ := repo.CreateQACase(context.Background(), e.s, p, repo.QACaseInput{SectionID: auth.ID, Title: "Session expires"}, nil)
	resp, _ = e.post(t, member, base+"/sections/"+auth.ID.String()+"/archive", nil, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != base+"/cases" {
		t.Fatalf("archive = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, _ := repo.GetQACase(context.Background(), e.s, c.ID)
	if got.ArchivedAt == nil || qaSectionNamed(t, e, p, "Auth").ArchivedAt == nil {
		t.Fatal("archive did not archive the section and its cases")
	}
	if n := auditCount(t, e.db, e.orgID, "qa.section.archive"); n != 1 {
		t.Fatalf("qa.section.archive audit events = %d; want 1", n)
	}
}

func TestQASections_PermissionsAndOwnership(t *testing.T) {
	e := newQAEnv(t, "acme-qasec-perm")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	guest, guestID := e.session(t, "client@bank.example", "guest")
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	repo.AddProjectMember(ctx, e.s, p, guestID)
	foreign, _ := repo.CreateQASection(ctx, e.s, other, "Invoices", nil)
	base := "/projects/" + p.String() + "/qa"

	for _, raw := range []string{viewer, guest} {
		if resp, _ := e.post(t, raw, base+"/sections", url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("read-only create = %d; want 403", resp.StatusCode)
		}
	}
	// Another project's section through this project's URL: 404, untouched.
	resp, _ := e.post(t, e.owner, base+"/sections/"+foreign.ID.String()+"/rename", url.Values{"name": {"Hijacked"}}, false)
	if resp.StatusCode != http.StatusNotFound || qaSectionNamed(t, e, other, "Invoices").ID != foreign.ID {
		t.Fatalf("cross-project rename = %d", resp.StatusCode)
	}
	resp, _ = e.post(t, e.owner, base+"/sections", url.Values{"name": {"Child"}, "parent_id": {foreign.ID.String()}}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("parent from another project = %d; want 404", resp.StatusCode)
	}
}

func TestQACases_BulkMoveIgnoresOtherProjects(t *testing.T) {
	e := newQAEnv(t, "acme-qamove")
	ctx := context.Background()
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	from, _ := repo.CreateQASection(ctx, e.s, p, "Inbox", nil)
	to, _ := repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	otherSec, _ := repo.CreateQASection(ctx, e.s, other, "Invoices", nil)
	a, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: from.ID, Title: "A"}, nil)
	b, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: from.ID, Title: "B"}, nil)
	x, _ := repo.CreateQACase(ctx, e.s, other, repo.QACaseInput{SectionID: otherSec.ID, Title: "X"}, nil)
	base := "/projects/" + p.String() + "/qa"

	form := url.Values{"case_id": {a.ID.String(), b.ID.String(), x.ID.String()}, "section_id": {to.ID.String()}}
	resp, _ := e.post(t, e.owner, base+"/cases/move", form, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bulk move = %d", resp.StatusCode)
	}
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		if c, _ := repo.GetQACase(ctx, e.s, id); c.SectionID != to.ID {
			t.Fatalf("case %s not moved", c.Title)
		}
	}
	if c, _ := repo.GetQACase(ctx, e.s, x.ID); c.SectionID != otherSec.ID {
		t.Fatal("another project's case was moved")
	}

	form.Set("section_id", otherSec.ID.String())
	if resp, _ := e.post(t, e.owner, base+"/cases/move", form, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("move into another project's section = %d; want 404", resp.StatusCode)
	}
	resp, body := e.post(t, e.owner, base+"/cases/move", url.Values{"section_id": {to.ID.String()}}, true)
	if !strings.Contains(body, "Select at least one case.") {
		t.Fatalf("empty selection = %d %s", resp.StatusCode, body)
	}
}

func TestQACases_WriteControlsOnlyForWriters(t *testing.T) {
	e := newQAEnv(t, "acme-qasec-ui")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	p := e.project(t, "Portal")
	auth, _ := repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	login, _ := repo.CreateQASection(ctx, e.s, p, "Login", &auth.ID)
	billing, _ := repo.CreateQASection(ctx, e.s, p, "Billing", nil)
	repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: auth.ID, Title: "Session expires"}, nil)
	path := "/projects/" + p.String() + "/qa/cases?section=" + auth.ID.String()

	_, body := e.get(t, e.owner, path)
	for _, want := range []string{"/qa/cases/new?section=" + auth.ID.String(), "New section", "Edit section…", `name="case_id"`, "Move to section…", `action="/projects/` + p.String() + `/qa/sections/` + auth.ID.String() + `/archive"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("owner page missing %q", want)
		}
	}
	// The move picker for Auth offers Billing but never Auth or its own subsection.
	move := body[strings.Index(body, `/sections/`+auth.ID.String()+`/move"`):]
	move = move[:strings.Index(move, "</form>")]
	if !strings.Contains(move, billing.ID.String()) || strings.Contains(move, login.ID.String()) {
		t.Fatal("move picker offers the section's own subtree or misses a valid target")
	}

	_, body = e.get(t, viewer, path)
	for _, gone := range []string{"/qa/cases/new", "New section", "Edit section…", `name="case_id"`, "Move to section…"} {
		if strings.Contains(body, gone) {
			t.Fatalf("viewer sees %q", gone)
		}
	}
}
