package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// A plan's progress is where each of its cases stands now: the case's item
// in the latest cycle that holds it.
func TestQAPlanProgress_LatestCycleWinsPerCase(t *testing.T) {
	f := newRunFixture(t, "acme-plan-progress")
	ctx := context.Background()
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil) // 3 cases
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "passed"})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[1].ID, Status: "failed"})
	retest, _ := repo.CreateQARetest(ctx, f.s, run.ID, nil)
	rItems, _ := repo.ListQARunItems(ctx, f.s, retest.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: rItems[0].ID, Status: "passed"})

	other, _ := repo.CreateQAPlan(ctx, f.s, f.projectID, repo.QAPlanInput{Name: "Empty", Phase: "sit"}, nil)
	got, err := repo.QAPlanProgress(ctx, f.s, []uuid.UUID{f.plan.ID, other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p := got[f.plan.ID]; p.Total != 3 || p.Passed != 2 || p.Untested != 1 || p.Failed != 0 {
		t.Fatalf("plan progress = %+v; want 3 cases: 2 passed (one after retest), 1 untested", p)
	}
	if p := got[other.ID]; p.Total != 0 {
		t.Fatalf("plan without runs = %+v", p)
	}
	runs, _ := repo.QARunProgressFor(ctx, f.s, []uuid.UUID{run.ID, retest.ID})
	if runs[run.ID].Failed != 1 || runs[retest.ID].Passed != 1 || runs[retest.ID].Total != 1 {
		t.Fatalf("run progress = %+v", runs)
	}
}

func TestQACaseRefs_KeyAndCurrentVersion(t *testing.T) {
	f := newRunFixture(t, "acme-case-refs")
	ctx := context.Background()
	in := repo.QACaseInput{SectionID: f.login.SectionID, Title: "Login, reworded", Steps: f.login.Steps}
	repo.UpdateQACase(ctx, f.s, f.login.ID, in)
	refs, err := repo.QACaseRefs(ctx, f.s, []uuid.UUID{f.login.ID, f.rfs.ID})
	if err != nil || refs[f.login.ID].Key != f.login.Key || refs[f.login.ID].Version != 2 || refs[f.rfs.ID].Version != 1 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}
