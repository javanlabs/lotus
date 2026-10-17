package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQARun_BulkAssignTwoHundredInOneRequest(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-assign")
	ctx := context.Background()
	sec, _ := repo.CreateQASection(ctx, r.s, r.projectID, "Bulk", nil)
	var ids []uuid.UUID
	for i := 0; i < 200; i++ {
		c, _ := repo.CreateQACase(ctx, r.s, r.projectID, repo.QACaseInput{SectionID: sec.ID, Title: fmt.Sprintf("Bulk %03d", i)}, nil)
		ids = append(ids, c.ID)
	}
	sc, _ := repo.CreateQAScenario(ctx, r.s, r.projectID, "Bulk", "regression", "", nil)
	repo.AddQAScenarioCases(ctx, r.s, sc.ID, nil, ids)
	plan, _ := repo.CreateQAPlan(ctx, r.s, r.projectID, repo.QAPlanInput{Name: "Bulk", Phase: "regression", ScenarioIDs: []uuid.UUID{sc.ID}}, nil)
	run, _ := repo.CreateQARun(ctx, r.s, plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, r.s, run.ID, repo.QAItemFilter{})
	_, foreign := r.firstRun(t)

	form := url.Values{"assignee_id": {r.guestID.String()}}
	for _, it := range items {
		form.Add("item_id", it.ID.String())
	}
	form.Add("item_id", foreign[0].ID.String())
	path := r.base + "/runs/" + run.ID.String()
	resp, _ := r.post(t, r.member, path+"/assign", form, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("assign = %d", resp.StatusCode)
	}
	_, per, _ := repo.QARunProgress(ctx, r.s, run.ID)
	if per[r.guestID].Total != 200 {
		t.Fatalf("assigned %d; want 200", per[r.guestID].Total)
	}
	if got, _ := repo.GetQARunItem(ctx, r.s, foreign[0].ID); got.AssigneeID != nil {
		t.Fatal("an item of another run was assigned")
	}

	form.Set("assignee_id", "")
	r.post(t, r.member, path+"/assign", form, true)
	if _, per, _ = repo.QARunProgress(ctx, r.s, run.ID); per[uuid.Nil].Total != 200 {
		t.Fatal("unassign did not clear")
	}
	viewer, viewerID := r.session(t, "viewer@acme.example", "viewer")
	form.Set("assignee_id", viewerID.String())
	if resp, _ := r.post(t, r.member, path+"/assign", form, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("assign to a viewer = %d; want 404", resp.StatusCode)
	}
	for _, raw := range []string{r.guest, viewer} {
		if resp, _ := r.post(t, raw, path+"/assign", url.Values{"item_id": {items[0].ID.String()}}, false); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("assign by a read/execute-only role = %d; want 403", resp.StatusCode)
		}
	}
	if _, body := r.get(t, r.member, path); !strings.Contains(body, `form="assign-items"`) {
		t.Fatal("writer has no assignment controls")
	}
	if _, body := r.get(t, r.guest, path); strings.Contains(body, `form="assign-items"`) {
		t.Fatal("guest sees assignment controls")
	}
}

func TestQARun_RetestAndClose(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-retest")
	ctx := context.Background()
	run, items := r.firstRun(t)
	path := r.base + "/runs/" + run.ID.String()

	_, body := r.post(t, r.member, path+"/retest", nil, true)
	if !strings.Contains(body, "Nothing failed or blocked to retest.") {
		t.Fatalf("retest with nothing failed: %s", body)
	}
	repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", CreatedBy: &r.guestID})
	resp, _ := r.post(t, r.member, path+"/retest", nil, true)
	runs, _ := repo.ListQARuns(ctx, r.s, r.plan.ID)
	retest := runs[len(runs)-1]
	if resp.Header.Get("HX-Redirect") != r.base+"/runs/"+retest.ID.String() || retest.RetestOf == nil || *retest.RetestOf != run.ID {
		t.Fatalf("retest = %q, %+v", resp.Header.Get("HX-Redirect"), retest)
	}
	rItems, _ := repo.ListQARunItems(ctx, r.s, retest.ID, repo.QAItemFilter{})
	if len(rItems) != 1 || rItems[0].AssigneeID == nil || *rItems[0].AssigneeID != r.guestID {
		t.Fatalf("retest items = %+v; want the failed one, given to who ran it", rItems)
	}
	if _, body = r.get(t, r.member, r.base+"/runs/"+retest.ID.String()); !strings.Contains(body, "retest of") || !strings.Contains(body, `href="`+path+`"`) {
		t.Fatal("retest page does not link back to its origin")
	}

	e := r.post
	e(t, r.member, path+"/close", nil, false)
	e(t, r.member, path+"/close", nil, false)
	if n := auditCount(t, r.db, r.orgID, "qa.run.close"); n != 1 {
		t.Fatalf("qa.run.close audit rows = %d; want 1", n)
	}
	if got, _ := repo.GetQARun(ctx, r.s, run.ID); got.ClosedAt == nil {
		t.Fatal("run not closed")
	}
	if _, body = r.get(t, r.member, path+"/items/"+items[1].ID.String()); strings.Contains(body, "Record result") {
		t.Fatal("a closed run still offers the result form")
	}
	if resp, _ := r.post(t, r.guest, path+"/close", nil, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest close = %d; want 403", resp.StatusCode)
	}
}

func TestQARun_SaveAsScenario(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-save")
	ctx := context.Background()
	viewer, _ := r.session(t, "viewer@acme.example", "viewer")
	run, _ := r.firstRun(t)
	path := r.base + "/runs/" + run.ID.String()

	if _, body := r.get(t, r.member, path); !strings.Contains(body, "Save as scenario…") {
		t.Fatal("no Save as scenario on the run page")
	}
	resp, _ := r.post(t, r.member, path+"/scenario", url.Values{"name": {"Regression core, as run"}, "phase": {"regression"}}, true)
	list, _ := repo.ListQAScenarios(ctx, r.s, r.projectID, false)
	var saved repo.QAScenario
	for _, sc := range list {
		if sc.Name == "Regression core, as run" {
			saved = sc
		}
	}
	if resp.Header.Get("HX-Redirect") != r.base+"/scenarios/"+saved.ID.String() {
		t.Fatalf("save = %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	if entries, _ := repo.ListQAScenarioEntries(ctx, r.s, saved.ID); len(entries) != 2 {
		t.Fatalf("saved scenario holds %d cases; want the run's 2", len(entries))
	}
	_, body := r.post(t, r.member, path+"/scenario", url.Values{"name": {"X"}, "phase": {"exploratory"}}, true)
	if !strings.Contains(body, "Choose a phase.") {
		t.Fatalf("bad phase: %s", body)
	}
	if resp, _ := r.post(t, viewer, path+"/scenario", url.Values{"name": {"X"}, "phase": {"uat"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer save = %d; want 403", resp.StatusCode)
	}
}
