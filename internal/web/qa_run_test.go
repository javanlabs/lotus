package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

type qaFile struct {
	name string
	body []byte
}

// record posts a result the way the item panel does: multipart, htmx.
func (r qaRunEnv) record(t *testing.T, raw string, runID, itemID uuid.UUID, status, comment string, files ...qaFile) (*http.Response, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("status", status)
	mw.WriteField("comment", comment)
	for _, f := range files {
		fw, _ := mw.CreateFormFile("file", f.name)
		fw.Write(f.body)
	}
	mw.Close()
	path := r.base + "/runs/" + runID.String() + "/items/" + itemID.String() + "/result"
	req := httptest.NewRequest(http.MethodPost, "http://"+r.host+path, &body)
	req.Host = r.host
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := r.app.Test(req, 60_000)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func (r qaRunEnv) firstRun(t *testing.T) (repo.QARun, []repo.QARunItem) {
	t.Helper()
	runs, _ := repo.ListQARuns(context.Background(), r.s, r.plan.ID)
	items, _ := repo.ListQARunItems(context.Background(), r.s, runs[0].ID, repo.QAItemFilter{})
	return runs[0], items
}

func TestQARun_PageShowsGroupsProgressAndFilters(t *testing.T) {
	r := newQARunEnv(t, "acme-qarun")
	ctx := context.Background()
	run, items := r.firstRun(t)
	repo.AssignQARunItems(ctx, r.s, run.ID, []uuid.UUID{items[0].ID}, &r.memberID)
	repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed"})
	path := r.base + "/runs/" + run.ID.String()

	status, body := r.get(t, r.member, path)
	if status != http.StatusOK || strings.Count(body, "data-item-row") != 2 || !strings.Contains(body, "TC-1") ||
		!strings.Contains(body, "1/2 done") || !strings.Contains(body, "qa@acme.example") {
		t.Fatalf("run page = %d:\n%s", status, body)
	}
	if _, body = r.get(t, r.member, path+"?who=mine"); strings.Count(body, "data-item-row") != 1 {
		t.Fatal("who=mine does not filter")
	}
	if _, body = r.get(t, r.member, path+"?who=unassigned"); strings.Count(body, "data-item-row") != 1 || !strings.Contains(body, "Logout") {
		t.Fatal("who=unassigned does not filter")
	}
	if _, body = r.get(t, r.member, path+"?status=failed"); strings.Count(body, "data-item-row") != 1 || !strings.Contains(body, "Login") {
		t.Fatal("status filter does not filter")
	}

	// The case changes after the run was created: the item says so.
	c, _ := repo.GetQACase(ctx, r.s, items[1].CaseID)
	repo.UpdateQACase(ctx, r.s, c.ID, repo.QACaseInput{SectionID: c.SectionID, Title: c.Title + " (v2)"})
	if _, body = r.get(t, r.member, path); !strings.Contains(body, "Case changed since this run") {
		t.Fatal("no changed-case badge")
	}
}

func TestQARun_RecordEachStatusAndRefuseTheRest(t *testing.T) {
	r := newQARunEnv(t, "acme-qarun-record")
	ctx := context.Background()
	run, items := r.firstRun(t)
	for _, st := range []string{"passed", "failed", "blocked", "skipped"} {
		resp, body := r.record(t, r.member, run.ID, items[0].ID, st, "note "+st)
		got, _ := repo.GetQARunItem(ctx, r.s, items[0].ID)
		if resp.StatusCode != http.StatusOK || got.Status != st || !strings.Contains(body, "note "+st) ||
			!strings.Contains(body, `id="run-progress" hx-swap-oob="true"`) {
			t.Fatalf("record %s = %d, item %s:\n%s", st, resp.StatusCode, got.Status, body)
		}
	}
	if hist, _ := repo.ListQAResults(ctx, r.s, items[0].ID); len(hist) != 4 || hist[0].Status != "skipped" {
		t.Fatalf("history = %+v", hist)
	}
	for _, st := range []string{"untested", "done", ""} {
		_, body := r.record(t, r.member, run.ID, items[1].ID, st, "")
		if !strings.Contains(body, "Choose a result: passed, failed, blocked or skipped.") {
			t.Fatalf("status %q: %s", st, body)
		}
	}
	repo.CloseQARun(ctx, r.s, run.ID)
	_, body := r.record(t, r.member, run.ID, items[1].ID, "passed", "")
	if !strings.Contains(body, "This run is closed.") {
		t.Fatalf("closed run: %s", body)
	}
}

func TestQARun_AttachmentsAreCheckedStoredAndServedSafely(t *testing.T) {
	r := newQARunEnv(t, "acme-qarun-files")
	ctx := context.Background()
	run, items := r.firstRun(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	resp, body := r.record(t, r.member, run.ID, items[0].ID, "failed", "500 on submit",
		qaFile{"shot.png", png}, qaFile{"trace.html", []byte("<script>alert(1)</script>")})
	if resp.StatusCode != http.StatusOK || strings.Count(body, "/qa/files/") != 2 {
		t.Fatalf("record with files = %d:\n%s", resp.StatusCode, body)
	}
	hist, _ := repo.ListQAResults(ctx, r.s, items[0].ID)
	files, _ := repo.ListQAAttachments(ctx, r.s, repo.QAAttachmentOwner{ResultID: &hist[0].ID})
	for _, f := range files {
		res, _ := r.app.Test(reqWithSession(http.MethodGet, r.host, r.base+"/files/"+f.ID.String(), r.member))
		disp := res.Header.Get("Content-Disposition")
		if res.StatusCode != http.StatusOK || res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("download %s = %d", f.FileName, res.StatusCode)
		}
		if f.FileName == "shot.png" && !strings.HasPrefix(disp, "inline") || f.FileName == "trace.html" && !strings.HasPrefix(disp, "attachment") {
			t.Fatalf("%s served with %q", f.FileName, disp)
		}
	}

	// Over 20 MB: refused before anything is recorded.
	big := bytes.Repeat([]byte("x"), repo.MaxQAAttachmentBytes+1)
	_, body = r.record(t, r.member, run.ID, items[1].ID, "passed", "", qaFile{"huge.log", big})
	if !strings.Contains(body, "Each file can be at most 20 MB.") {
		t.Fatalf("oversized file: %s", body)
	}
	if got, _ := repo.GetQARunItem(ctx, r.s, items[1].ID); got.Status != "untested" {
		t.Fatal("a refused upload still recorded the result")
	}

	// Another project's attachment through this project's URL: 404.
	other := r.project(t, "Billing")
	if res, _ := r.app.Test(reqWithSession(http.MethodGet, r.host, r.projectPath(t, other)+"/qa/files/"+files[0].ID.String(), r.member)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("attachment via another project = %d; want 404", res.StatusCode)
	}
}

func TestQARun_WhoMayRecord(t *testing.T) {
	r := newQARunEnv(t, "acme-qarun-who")
	ctx := context.Background()
	viewer, _ := r.session(t, "viewer@acme.example", "viewer")
	run, items := r.firstRun(t)

	if resp, _ := r.record(t, r.guest, run.ID, items[0].ID, "passed", "OK di UAT"); resp.StatusCode != http.StatusOK {
		t.Fatalf("guest on its project = %d; want 200", resp.StatusCode)
	}
	if resp, _ := r.record(t, viewer, run.ID, items[0].ID, "passed", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer = %d; want 403", resp.StatusCode)
	}
	// An item of another run through this run's URL: 404.
	other, _ := repo.CreateQARun(ctx, r.s, r.plan.ID, repo.QARunInput{Name: "Cycle 2"}, nil)
	otherItems, _ := repo.ListQARunItems(ctx, r.s, other.ID, repo.QAItemFilter{})
	if resp, _ := r.record(t, r.member, run.ID, otherItems[0].ID, "passed", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("item of another run = %d; want 404", resp.StatusCode)
	}
	// A guest listed elsewhere cannot open this project's run.
	stranger, strangerID := r.session(t, "other-client@bank.example", "guest")
	elsewhere := r.project(t, "Elsewhere")
	repo.AddProjectMember(ctx, r.s, elsewhere, strangerID)
	if status, _ := r.get(t, stranger, r.base+"/runs/"+run.ID.String()); status != http.StatusNotFound {
		t.Fatalf("guest of another project = %d; want 404", status)
	}
}
