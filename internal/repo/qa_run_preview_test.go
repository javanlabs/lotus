package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestPreviewQARun_CountsWhoGetsWhatAndWritesNothing(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-preview") // 3 distinct cases across UAT (2) and Regression (2, one shared)
	ctx := context.Background()
	a, b := mustUser(t, f.db, "a@acme.example"), mustUser(t, f.db, "b@acme.example")

	p, err := repo.PreviewQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Testers: []uuid.UUID{a, b}, Distribution: repo.DistributeRoundRobin})
	if err != nil || p.Cases != 3 || p.PerTester[a] != 2 || p.PerTester[b] != 1 || p.Unassigned != 0 {
		t.Fatalf("round robin preview = %+v, %v", p, err)
	}
	p, _ = repo.PreviewQARun(ctx, f.s, f.plan.ID, repo.QARunInput{})
	if p.Cases != 3 || p.Unassigned != 3 {
		t.Fatalf("no distribution = %+v", p)
	}
	p, _ = repo.PreviewQARun(ctx, f.s, f.plan.ID, repo.QARunInput{ScenarioIDs: []uuid.UUID{f.uat.ID}, Testers: []uuid.UUID{a, b}, Distribution: repo.DistributeByGroup})
	if p.Cases != 2 || p.PerTester[a] != 1 || p.PerTester[b] != 1 {
		t.Fatalf("by group preview = %+v; the UAT scenario has an ungrouped case and a Day 2 case", p)
	}
	if runs, _ := repo.ListQARuns(ctx, f.s, f.plan.ID); len(runs) != 0 {
		t.Fatal("preview created a run")
	}

	empty, _ := repo.CreateQAPlan(ctx, f.s, f.projectID, repo.QAPlanInput{Name: "Empty", Phase: "sit"}, nil)
	if p, err := repo.PreviewQARun(ctx, f.s, empty.ID, repo.QARunInput{}); err != nil || p.Cases != 0 {
		t.Fatalf("empty plan preview = %+v, %v", p, err)
	}
}
