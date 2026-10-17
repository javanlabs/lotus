package repo_test

import (
	"context"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAIssues_FromItemTimelineAndKeys(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-iss")
	ctx := context.Background()
	qa := mustUser(t, f.db, "qa@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})

	iss, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{
		Title: "Submit returns 500", Severity: "critical", Source: "internal",
	}, &qa, &items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Key != 1 || iss.Status != "new" || iss.PlanID == nil || *iss.PlanID != f.plan.ID {
		t.Fatalf("issue = %+v", iss)
	}
	linked, _ := repo.ListQAIssuesForItem(ctx, f.s, items[0].ID)
	if len(linked) != 1 || linked[0].ID != iss.ID {
		t.Fatalf("linked = %+v", linked)
	}
	if _, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{Title: "x", Severity: "urgent"}, nil, nil); err != repo.ErrInvalidInput {
		t.Fatalf("bad severity: %v", err)
	}

	repo.CommentQAIssue(ctx, f.s, iss.ID, "Repro on staging too", &qa)
	repo.ChangeQAIssueStatus(ctx, f.s, iss.ID, "fixed", &qa, "Deployed 1.4.1")
	repo.ChangeQAIssueStatus(ctx, f.s, iss.ID, "fixed", &qa, "") // unchanged: no event
	events, _ := repo.ListQAIssueEvents(ctx, f.s, iss.ID)
	if len(events) != 2 || events[0].Kind != "comment" || events[1].Kind != "status" ||
		*events[1].StatusFrom != "new" || *events[1].StatusTo != "fixed" || events[1].Body != "Deployed 1.4.1" {
		t.Fatalf("events = %+v", events)
	}
	got, _ := repo.GetQAIssueByKey(ctx, f.s, f.projectID, 1)
	if got.Status != "fixed" {
		t.Fatalf("status = %q", got.Status)
	}
	if _, err := repo.CommentQAIssue(ctx, f.s, iss.ID, "   ", &qa); err != repo.ErrInvalidInput {
		t.Fatalf("blank comment: %v", err)
	}
}
