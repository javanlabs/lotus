package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// Final review, Important 1: the picker must reach past the cases a large
// scenario already holds.
func TestQAScenarioPicker_ReachesCasesPastTheOnesHeld(t *testing.T) {
	e := newQAEnv(t, "acme-rf-picker")
	ctx := context.Background()
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "All", nil)
	var held []uuid.UUID
	for i := 1; i <= 250; i++ {
		c, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: fmt.Sprintf("Case %03d", i)}, nil)
		if i <= 220 {
			held = append(held, c.ID)
		}
	}
	sc, _ := repo.CreateQAScenario(ctx, e.s, p, "Regression", "regression", "", nil)
	repo.AddQAScenarioCases(ctx, e.s, sc.ID, nil, held)

	_, body := e.get(t, e.owner, "/projects/"+p.String()+"/qa/scenarios/"+sc.ID.String()+"/picker")
	if n := strings.Count(body, `name="case_id"`); n != 30 || !strings.Contains(body, "Case 221") {
		t.Fatalf("picker lists %d cases; want the 30 not yet held", n)
	}
}

// Final review, Important 2: nothing goes into an archived section, and a
// case filed in one is restored by moving it out first.
func TestQACase_ArchivedSectionRefusesNewRowsAndRestore(t *testing.T) {
	e := newQAEnv(t, "acme-rf-archsec")
	ctx := context.Background()
	p := e.project(t, "Portal")
	live, _ := repo.CreateQASection(ctx, e.s, p, "Live", nil)
	old, _ := repo.CreateQASection(ctx, e.s, p, "Old", nil)
	repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: old.ID, Title: "Kept"}, nil)
	other, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: live.ID, Title: "Other"}, nil)
	repo.ArchiveQASection(ctx, e.s, old.ID)
	base := "/projects/" + p.String() + "/qa"

	_, body := e.get(t, e.owner, base+"/cases/1")
	if strings.Contains(body, ">Restore<") || !strings.Contains(body, "Its section is archived") {
		t.Fatalf("case in an archived section offers Restore:\n%s", body)
	}
	if resp, _ := e.post(t, e.owner, base+"/cases/1/restore", nil, false); resp.StatusCode != http.StatusConflict {
		t.Fatalf("restore = %d; want 409", resp.StatusCode)
	}
	resp, body := e.post(t, e.owner, base+"/cases/new", url.Values{"title": {"New"}, "section_id": {old.ID.String()}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "That section is archived. Choose another.") {
		t.Fatalf("new case into archived section = %d", resp.StatusCode)
	}
	_, body = e.post(t, e.owner, base+"/sections", url.Values{"name": {"Child"}, "parent_id": {old.ID.String()}}, true)
	if !strings.Contains(body, "That section is archived.") {
		t.Fatalf("section under archived parent: %s", body)
	}
	_, body = e.post(t, e.owner, base+"/cases/move", url.Values{"case_id": {other.ID.String()}, "section_id": {old.ID.String()}}, true)
	if !strings.Contains(body, "That section is archived.") {
		t.Fatalf("bulk move into archived section: %s", body)
	}
}

// Final review, Important 3: an archived scenario refuses edits, and
// archiving twice records one audit event (cases likewise).
func TestQAArchive_IsFinalAndAuditedOnce(t *testing.T) {
	e := newQAEnv(t, "acme-rf-archive")
	ctx := context.Background()
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "All", nil)
	repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: "Login"}, nil)
	sc, _ := repo.CreateQAScenario(ctx, e.s, p, "UAT", "uat", "", nil)
	base := "/projects/" + p.String() + "/qa"

	e.post(t, e.owner, base+"/scenarios/"+sc.ID.String()+"/archive", nil, false)
	e.post(t, e.owner, base+"/scenarios/"+sc.ID.String()+"/archive", nil, false)
	_, body := e.post(t, e.owner, base+"/scenarios/"+sc.ID.String(), url.Values{"name": {"Renamed"}, "phase": {"uat"}}, true)
	got, _ := repo.GetQAScenario(ctx, e.s, sc.ID)
	if got.Name != "UAT" || !strings.Contains(body, "This scenario is archived.") {
		t.Fatalf("edit of an archived scenario: name %q, body %s", got.Name, body)
	}
	if n := auditCount(t, e.db, e.orgID, "qa.scenario.archive"); n != 1 {
		t.Fatalf("scenario archive audit rows = %d; want 1", n)
	}
	e.post(t, e.owner, base+"/cases/1/archive", nil, false)
	e.post(t, e.owner, base+"/cases/1/archive", nil, false)
	if n := auditCount(t, e.db, e.orgID, "qa.case.archive"); n != 1 {
		t.Fatalf("case archive audit rows = %d; want 1", n)
	}
}

// Final review, Important 4: archiving a section from its dialog leaves
// the section's own URL; that URL, visited later, shows the archived view.
func TestQASections_ArchiveLeavesTheArchivedSection(t *testing.T) {
	e := newQAEnv(t, "acme-rf-archnav")
	ctx := context.Background()
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "Old", nil)
	base := "/projects/" + p.String() + "/qa"

	resp, _ := e.post(t, e.owner, base+"/sections/"+sec.ID.String()+"/archive", nil, true)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != base+"/cases" {
		t.Fatalf("archive from the dialog = %d, HX-Redirect %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	r, _ := e.app.Test(reqWithSession(http.MethodGet, e.host, base+"/cases?section="+sec.ID.String(), e.owner))
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != base+"/cases?archived=1&section="+sec.ID.String() {
		t.Fatalf("archived section URL = %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

// Final review, Minor 6 (re-graded Important): the confirm form posts the
// file as multipart, so a file the preview accepted fits the confirm too.
func TestQACaseImport_ConfirmPostsMultipart(t *testing.T) {
	e := newQAEnv(t, "acme-rf-multipart")
	p := e.project(t, "Portal")
	base := "/projects/" + p.String() + "/qa/cases/import"
	// About 3.6 MB: under the 4 MB limit as a file, over it url-encoded.
	var b strings.Builder
	b.WriteString(qaCSVHead)
	long := strings.Repeat("Klik tombol & isi form, lalu cek hasil. ", 18)
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "Area,Case %d,\"%s\",,,,\n", i, long)
	}
	_, body := e.upload(t, e.owner, base, b.String())
	if !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Fatal("confirm form is not multipart")
	}
	var mb bytes.Buffer
	mw := multipart.NewWriter(&mb)
	mw.WriteField("csv", confirmField(t, body))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://"+e.host+base+"/confirm", &mb)
	req.Host = e.host
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: e.owner})
	resp, err := e.app.Test(req, 30_000)
	if err != nil || resp.StatusCode != http.StatusFound {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("multipart confirm = %v %v %s", resp.StatusCode, err, out)
	}
}

// Phase 5 final review, Important 2: editing a plan must not drop an
// archived scenario it holds, and runs leave archived scenarios out, as
// they leave archived cases out.
func TestQAPlans_ArchivedScenarioStaysOnEditAndRunsSkipIt(t *testing.T) {
	r := newQARunEnv(t, "acme-rf-archscen")
	ctx := context.Background()
	scs := mustPlanScenarios(t, r.qaEnv, r.plan.ID)
	repo.ArchiveQAScenario(ctx, r.s, scs[0])
	path := r.base + "/plans/" + r.plan.ID.String()

	_, body := r.get(t, r.member, path)
	if !strings.Contains(body, `value="`+scs[0].String()+`" checked`) || !strings.Contains(body, "archived") {
		t.Fatal("the edit form does not keep the plan's archived scenario ticked")
	}
	// The form as the browser sends it: the archived scenario still ticked.
	r.post(t, r.member, path, url.Values{"name": {"Regression October, renamed"}, "phase": {"regression"}, "scenario_id": {scs[0].String()}}, true)
	if got := mustPlanScenarios(t, r.qaEnv, r.plan.ID); len(got) != 1 {
		t.Fatalf("plan scenarios after a rename = %v; want the archived one kept", got)
	}
	p, err := repo.PreviewQARun(ctx, r.s, r.plan.ID, repo.QARunInput{})
	if err != nil || p.Cases != 0 {
		t.Fatalf("preview over an archived scenario = %+v, %v; want no cases", p, err)
	}
}

// Phase 6 final review, Important 1: an issue whose assignee can no longer
// be assigned (a viewer now, gone, an imported account) still saves when
// the assignee is left as it is; another invalid assignee gets a message.
func TestQAIssue_EditKeepsAStaleAssignee(t *testing.T) {
	r := newQARunEnv(t, "acme-rf-stale")
	ctx := context.Background()
	_, viewerID := r.session(t, "viewer@acme.example", "viewer")
	is, _ := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{Title: "Old", Severity: "minor", AssigneeID: &viewerID}, nil, nil)
	path := fmt.Sprintf("%s/issues/%d", r.base, is.Key)

	_, body := r.get(t, r.member, path)
	if !strings.Contains(body, "no longer assignable") {
		t.Fatal("the edit dialog does not show the current, stale assignee")
	}
	resp, _ := r.post(t, r.member, path, url.Values{"title": {"Renamed"}, "severity": {"minor"}, "assignee_id": {viewerID.String()}}, true)
	got, _ := repo.GetQAIssue(ctx, r.s, is.ID)
	if resp.StatusCode != http.StatusNoContent || got.Title != "Renamed" || got.AssigneeID == nil || *got.AssigneeID != viewerID {
		t.Fatalf("edit keeping a stale assignee = %d, %+v", resp.StatusCode, got)
	}
	_, other := r.session(t, "viewer2@acme.example", "viewer")
	_, body = r.post(t, r.member, path, url.Values{"title": {"Renamed"}, "severity": {"minor"}, "assignee_id": {other.String()}}, true)
	if !strings.Contains(body, "That person cannot be assigned here.") {
		t.Fatalf("a new invalid assignee: %s", body)
	}
}

// Phase 6 final review, Important 3: "Change status" to the status the
// issue already has says so rather than dropping the typed note.
func TestQAIssue_UnchangedStatusIsRefusedWithAMessage(t *testing.T) {
	r := newQARunEnv(t, "acme-rf-samestatus")
	ctx := context.Background()
	is, _ := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{Title: "X", Severity: "minor"}, nil, nil)
	path := fmt.Sprintf("%s/issues/%d/status", r.base, is.Key)
	_, body := r.post(t, r.member, path, url.Values{"status": {"new"}, "note": {"Looked at it"}}, true)
	if !hasText(body, fmt.Sprintf("BUG-%d is already new. Pick another status, or add the note as a comment.", is.Key)) {
		t.Fatalf("unchanged status: %s", body)
	}
	if events, _ := repo.ListQAIssueEvents(ctx, r.s, is.ID); len(events) != 0 {
		t.Fatalf("events = %d; want none", len(events))
	}
}
