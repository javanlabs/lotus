package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAItems_ResultUpdatesItemAndHistory(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-res")
	ctx := context.Background()
	tester := mustUser(t, f.db, "tester@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	it := items[0]

	if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: it.ID, Status: "untested"}); err != repo.ErrInvalidInput {
		t.Fatalf("untested result: %v; want ErrInvalidInput", err)
	}
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: it.ID, Status: "failed", Comment: "500 on submit", CreatedBy: &tester})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: it.ID, Status: "passed", CreatedBy: &tester})

	got, _ := repo.GetQARunItem(ctx, f.s, it.ID)
	if got.Status != "passed" || got.ExecutedBy == nil || *got.ExecutedBy != tester || got.StatusAt == nil {
		t.Fatalf("item after results = %+v", got)
	}
	hist, _ := repo.ListQAResults(ctx, f.s, it.ID)
	if len(hist) != 2 || hist[0].Status != "passed" || hist[1].Comment != "500 on submit" {
		t.Fatalf("history = %+v", hist)
	}

	repo.CloseQARun(ctx, f.s, run.ID)
	if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: it.ID, Status: "failed"}); err != repo.ErrQAClosed {
		t.Fatalf("result on closed run: %v; want ErrQAClosed", err)
	}
}

func TestQAItems_BulkAssignProgressAndMyWork(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-assign")
	ctx := context.Background()
	a := mustUser(t, f.db, "a@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})

	n, err := repo.AssignQARunItems(ctx, f.s, run.ID, []uuid.UUID{items[0].ID, items[1].ID}, &a)
	if err != nil || n != 2 {
		t.Fatalf("assign = %d, %v", n, err)
	}
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "passed", CreatedBy: &a})

	total, per, _ := repo.QARunProgress(ctx, f.s, run.ID)
	if total.Total != 3 || total.Passed != 1 || total.Untested != 2 || total.Done() != 1 {
		t.Fatalf("total = %+v", total)
	}
	if per[a].Total != 2 || per[a].Passed != 1 || per[uuid.Nil].Total != 1 {
		t.Fatalf("per assignee = %+v", per)
	}
	mine, _ := repo.ListMyQAItems(ctx, f.s, a, nil)
	if len(mine) != 1 || mine[0].ID != items[1].ID || mine[0].PlanName != "UAT September" {
		t.Fatalf("my items = %+v", mine)
	}
	// Scoped to another project: nothing.
	mine, _ = repo.ListMyQAItems(ctx, f.s, a, []uuid.UUID{uuid.New()})
	if len(mine) != 0 {
		t.Fatalf("my items in unrelated project = %d; want 0", len(mine))
	}
}
