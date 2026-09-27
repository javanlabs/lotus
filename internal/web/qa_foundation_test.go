package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// Attachments go up to 20 MB (repo.MaxQAAttachmentBytes), so the server
// must take a request of that size to the handler rather than refusing it
// as 413 first.
func TestServer_TakesRequestsLargerThanFiberDefault(t *testing.T) {
	e := newQAEnv(t, "acme-bodylimit")
	p := e.project(t, "Portal")
	big := strings.Repeat("x", 6<<20) // over Fiber's 4 MB default
	resp, _ := e.upload(t, e.owner, "/projects/"+p.String()+"/qa/cases/import", "not,a,header\n"+big)
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
	_, body := e.get(t, e.owner, "/projects/"+p.String()+"/qa/scenarios/"+list[0].ID.String())
	if !strings.Contains(body, "1 case is archived and is skipped when a run is created.") || strings.Contains(body, "copied into new runs") {
		t.Fatalf("archived-case note is wrong:\n%s", body)
	}
}
