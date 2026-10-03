package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// Attachments go up to 20 MB (repo.MaxQAAttachmentBytes), so the server
// must take a request of that size to the handler rather than refusing it
// as 413 first.
func TestServer_TakesRequestsLargerThanFiberDefault(t *testing.T) {
	e := newQAEnv(t, "acme-bodylimit")
	p := e.project(t, "Portal")
	big := strings.Repeat("x", 6<<20) // over Fiber's 4 MB default
	resp, _ := e.upload(t, e.owner, e.projectPath(t, p)+"/qa/cases/import", "not,a,header\n"+big)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("6 MB upload = %d; want the handler's 422, not 413", resp.StatusCode)
	}
}

// Runs leave archived cases out; the scenario page says so.
func TestQAScenario_SaysArchivedCasesAreSkipped(t *testing.T) {
	e := newQAEnv(t, "acme-archived-copy")
	ctx := context.Background()
	p := e.project(t, "Portal")
	_, cases := seedQALibrary(t, e, p)
	repo.ArchiveQACase(ctx, e.s, cases[1].ID)
	list, _ := repo.ListQAScenarios(ctx, e.s, p, false)
	_, body := e.get(t, e.owner, e.projectPath(t, p)+"/qa/scenarios/"+list[0].ID.String())
	if !strings.Contains(body, "1 case is archived and is skipped when a run is created.") || strings.Contains(body, "copied into new runs") {
		t.Fatalf("archived-case note is wrong:\n%s", body)
	}
}

// Without a blob store (LOTUS_BLOB_DIR could not be created at start) the
// app still serves: results without files are recorded, anything that
// needs file storage says it is unavailable.
func TestQARun_WithoutBlobStoreOnlyFilesAreUnavailable(t *testing.T) {
	r := newQARunEnvWith(t, "acme-noblob", func(d *Deps) { d.Blob = nil })
	run, items := r.firstRun(t)
	if status, _ := r.get(t, r.member, r.base+"/runs/"+run.ID.String()); status != http.StatusOK {
		t.Fatalf("run page = %d", status)
	}
	if resp, _ := r.record(t, r.member, run.ID, items[0].ID, "passed", "no files"); resp.StatusCode != http.StatusOK {
		t.Fatalf("result without files = %d; want 200", resp.StatusCode)
	}
	_, body := r.record(t, r.member, run.ID, items[1].ID, "failed", "", qaFile{"shot.png", []byte("\x89PNG\r\n\x1a\n")})
	if !strings.Contains(body, "File storage is not available. Record the result without files, or ask an administrator.") {
		t.Fatalf("result with files: %s", body)
	}
	if got, _ := repo.GetQARunItem(context.Background(), r.s, items[1].ID); got.Status != "untested" {
		t.Fatal("a result with files was recorded without its files")
	}
	if status, _ := r.get(t, r.member, r.base+"/files/"+uuid.NewString()); status != http.StatusServiceUnavailable {
		t.Fatalf("download without a blob store = %d; want 503", status)
	}
}
