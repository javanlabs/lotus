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

func TestQAScenarios_ListCreateEditArchive(t *testing.T) {
	e := newQAEnv(t, "acme-qascen")
	ctx := context.Background()
	member, _ := e.session(t, "qa@acme.example", "member")
	p := e.project(t, "Portal")
	_, cases := seedQALibrary(t, e, p) // "Regression core" holds both cases
	repo.ArchiveQACase(ctx, e.s, cases[1].ID)
	base := "/projects/" + p.String() + "/qa/scenarios"

	status, body := e.get(t, member, base)
	if status != http.StatusOK || !strings.Contains(body, "Regression core") || !strings.Contains(body, ">Regression<") ||
		!strings.Contains(body, "1 archived") {
		t.Fatalf("list = %d:\n%s", status, body)
	}

	resp, _ := e.post(t, member, base, url.Values{"name": {"UAT <b>RFS</b>"}, "phase": {"uat"}, "description": {"Client sign-off"}}, true)
	list, _ := repo.ListQAScenarios(ctx, e.s, p, false)
	var uat repo.QAScenario
	for _, sc := range list {
		if sc.Phase == "uat" {
			uat = sc
		}
	}
	// The dialog posts with htmx; it is sent on to the new scenario.
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != base+"/"+uat.ID.String() || uat.Name != "UAT <b>RFS</b>" {
		t.Fatalf("create = %d HX-Redirect %q, %+v", resp.StatusCode, resp.Header.Get("HX-Redirect"), uat)
	}
	status, body = e.get(t, member, base+"/"+uat.ID.String())
	if status != http.StatusOK || strings.Contains(body, "<b>RFS</b>") || !strings.Contains(body, "UAT &lt;b&gt;RFS&lt;/b&gt;") {
		t.Fatalf("detail = %d or unescaped", status)
	}

	resp, body = e.post(t, member, base, url.Values{"name": {"X"}, "phase": {"exploratory"}}, true)
	if !strings.Contains(body, "Choose a phase.") {
		t.Fatalf("bad phase = %d %s", resp.StatusCode, body)
	}
	resp, _ = e.post(t, member, base+"/"+uat.ID.String(), url.Values{"name": {"UAT RFS"}, "phase": {"uat"}, "description": {""}}, true)
	if got, _ := repo.GetQAScenario(ctx, e.s, uat.ID); resp.StatusCode != http.StatusNoContent || got.Name != "UAT RFS" {
		t.Fatalf("edit = %d, %+v", resp.StatusCode, got)
	}
	resp, _ = e.post(t, member, base+"/"+uat.ID.String()+"/archive", nil, false)
	if got, _ := repo.GetQAScenario(ctx, e.s, uat.ID); resp.StatusCode != http.StatusFound || got.ArchivedAt == nil ||
		auditCount(t, e.db, e.orgID, "qa.scenario.archive") != 1 {
		t.Fatalf("archive = %d", resp.StatusCode)
	}
	if _, body = e.get(t, member, base); strings.Contains(body, "UAT RFS") {
		t.Fatal("archived scenario listed by default")
	}
	if _, body = e.get(t, member, base+"?archived=1"); !strings.Contains(body, "UAT RFS") {
		t.Fatal("archived=1 hides the archived scenario")
	}
}

func TestQAScenarios_OwnershipAndPermissions(t *testing.T) {
	e := newQAEnv(t, "acme-qascen-perm")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	foreign, _ := repo.CreateQAScenario(ctx, e.s, other, "Billing SIT", "sit", "", nil)
	base := "/projects/" + p.String() + "/qa/scenarios"

	for _, path := range []string{base + "/" + foreign.ID.String(), base + "/" + uuid.NewString(), base + "/nope"} {
		if status, _ := e.get(t, e.owner, path); status != http.StatusNotFound {
			t.Fatalf("GET %s = %d; want 404", path, status)
		}
	}
	if resp, _ := e.post(t, e.owner, base+"/"+foreign.ID.String()+"/archive", nil, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("archive another project's scenario = %d; want 404", resp.StatusCode)
	}
	if resp, _ := e.post(t, viewer, base, url.Values{"name": {"X"}, "phase": {"sit"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create = %d; want 403", resp.StatusCode)
	}
	if _, body := e.get(t, viewer, base); strings.Contains(body, "New scenario") {
		t.Fatal("viewer sees New scenario")
	}
}
