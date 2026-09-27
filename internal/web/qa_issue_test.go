package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// comment posts the comment form as the browser does (multipart).
func (r qaRunEnv) comment(t *testing.T, raw string, key string, body string, files ...qaFile) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("body", body)
	for _, f := range files {
		fw, _ := mw.CreateFormFile("file", f.name)
		fw.Write(f.body)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://"+r.host+r.base+"/issues/"+key+"/comments", &buf)
	req.Host = r.host
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := r.app.Test(req, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// issueFromItem reports BUG-2 from the run's first item, which failed.
func issueFromItem(t *testing.T, r qaRunEnv) (*repo.QAIssue, repo.QARun) {
	t.Helper()
	ctx := context.Background()
	run, items := r.firstRun(t)
	repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed"})
	is, err := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{Title: "Submit <i>fails</i>", Severity: "major",
		Description: "500 on submit", StepsToReproduce: "1. Open\n2. Submit"}, &r.memberID, &items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return is, run
}

func TestQAIssue_PageShowsFieldsLinksAndTimeline(t *testing.T) {
	r := newQARunEnv(t, "acme-qaissue-page")
	ctx := context.Background()
	is, run := issueFromItem(t, r)
	repo.ChangeQAIssueStatus(ctx, r.s, is.ID, "open", &r.memberID, "Confirmed on staging")
	repo.CommentQAIssue(ctx, r.s, is.ID, "Also <b>on</b> mobile", &r.guestID)

	status, body := r.get(t, r.guest, r.base+"/issues/2")
	if status != http.StatusOK {
		t.Fatalf("issue page = %d", status)
	}
	for _, want := range []string{"BUG-2", "Submit &lt;i&gt;fails&lt;/i&gt;", "500 on submit", "Confirmed on staging", "new → open",
		"Also &lt;b&gt;on&lt;/b&gt; mobile", `href="` + r.base + `/runs/` + run.ID.String() + `"`, "Regression October"} {
		if !strings.Contains(body, want) {
			t.Fatalf("issue page missing %q", want)
		}
	}
	if strings.Contains(body, "<i>fails</i>") || strings.Contains(body, "<b>on</b>") {
		t.Fatal("markup rendered unescaped")
	}
	if status, _ := r.get(t, r.guest, r.base+"/issues/99"); status != http.StatusNotFound {
		t.Fatalf("unknown key = %d; want 404", status)
	}
}

func TestQAIssue_StatusEditAndCommentPermissions(t *testing.T) {
	r := newQARunEnv(t, "acme-qaissue-perm")
	ctx := context.Background()
	is, _ := issueFromItem(t, r)
	path := r.base + "/issues/2"

	resp, _ := r.post(t, r.member, path+"/status", url.Values{"status": {"fixed"}, "note": {"Deployed 1.4.1"}}, false)
	r.post(t, r.member, path+"/status", url.Values{"status": {"fixed"}, "note": {"again"}}, false)
	events, _ := repo.ListQAIssueEvents(ctx, r.s, is.ID)
	if resp.StatusCode != http.StatusFound || len(events) != 1 || *events[0].StatusTo != "fixed" || events[0].Body != "Deployed 1.4.1" {
		t.Fatalf("status change = %d, events %+v; want exactly one", resp.StatusCode, events)
	}
	_, body := r.post(t, r.member, path+"/status", url.Values{"status": {"done"}}, true)
	if !strings.Contains(body, "Choose a status.") {
		t.Fatalf("bad status: %s", body)
	}

	edit := url.Values{"title": {"Submit fails on RFS"}, "severity": {"blocker"}, "assignee_id": {r.guestID.String()}}
	if resp, _ := r.post(t, r.member, path, edit, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("edit = %d", resp.StatusCode)
	}
	got, _ := repo.GetQAIssue(ctx, r.s, is.ID)
	if got.Title != "Submit fails on RFS" || got.Severity != "blocker" || got.AssigneeID == nil || *got.AssigneeID != r.guestID || got.Source != "internal" {
		t.Fatalf("edited issue = %+v", got)
	}
	viewer, viewerID := r.session(t, "viewer@acme.example", "viewer")
	edit.Set("assignee_id", viewerID.String())
	if resp, _ := r.post(t, r.member, path, edit, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("assign to a viewer = %d; want 404", resp.StatusCode)
	}

	// A guest comments (with a file) but cannot change status or edit.
	if resp, _ := r.comment(t, r.guest, "2", "Masih terjadi di UAT", qaFile{"layar.png", []byte("\x89PNG\r\n\x1a\n")}); resp.StatusCode != http.StatusFound {
		t.Fatalf("guest comment = %d", resp.StatusCode)
	}
	events, _ = repo.ListQAIssueEvents(ctx, r.s, is.ID)
	last := events[len(events)-1]
	if files, _ := repo.ListQAAttachments(ctx, r.s, repo.QAAttachmentOwner{EventID: &last.ID}); last.Kind != "comment" || len(files) != 1 {
		t.Fatalf("guest comment = %+v, files %d", last, len(files))
	}
	for _, p := range []string{path + "/status", path} {
		if resp, _ := r.post(t, r.guest, p, url.Values{"status": {"closed"}, "title": {"x"}, "severity": {"trivial"}}, false); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("guest POST %s = %d; want 403", p, resp.StatusCode)
		}
	}
	if resp, _ := r.comment(t, viewer, "2", "hi"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer comment = %d; want 403", resp.StatusCode)
	}
	if _, body := r.comment(t, r.member, "2", "   "); !strings.Contains(body, "Write a comment.") {
		t.Fatalf("empty comment: %s", body)
	}

	// The guest's file is served only through its own project.
	files, _ := repo.ListQAAttachments(ctx, r.s, repo.QAAttachmentOwner{EventID: &last.ID})
	other := r.project(t, "Billing")
	if res, _ := r.app.Test(reqWithSession(http.MethodGet, r.host, "/projects/"+other.String()+"/qa/files/"+files[0].ID.String(), r.member)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("file via another project = %d; want 404", res.StatusCode)
	}
	if res, _ := r.app.Test(reqWithSession(http.MethodGet, r.host, r.base+"/files/"+files[0].ID.String(), r.guest)); res.StatusCode != http.StatusOK {
		t.Fatalf("file via its project = %d; want 200", res.StatusCode)
	}
}
