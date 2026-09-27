package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQASummaries_CountLiveRowsPerProject(t *testing.T) {
	f := newRunFixture(t, "acme-qa-summary")
	ctx := context.Background()
	iss, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{Title: "x", Severity: "minor", Source: "internal"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ArchiveQACase(ctx, f.s, f.report.ID); err != nil {
		t.Fatal(err)
	}
	empty := mustProject(t, f.s, "empty", nil)
	gone := mustProject(t, f.s, "gone", nil)
	if err := repo.SetProjectArchived(ctx, f.s, gone.ID, true); err != nil {
		t.Fatal(err)
	}

	all, err := repo.ListQAProjectSummaries(ctx, f.s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("summaries = %+v; want the portal and the empty project, not the archived one", all)
	}
	byID := map[uuid.UUID]repo.QAProjectSummary{}
	for _, s := range all {
		byID[s.ProjectID] = s
	}
	p := byID[f.projectID]
	if p.Cases != 2 || p.Scenarios != 2 || p.OpenPlans != 1 || p.OpenIssues != 1 || p.ProjectName != "portal" {
		t.Fatalf("portal summary = %+v", p)
	}
	if e := byID[empty.ID]; e.Cases != 0 || e.Scenarios != 0 || e.OpenPlans != 0 || e.OpenIssues != 0 {
		t.Fatalf("empty summary = %+v", e)
	}

	// A guest passes its project list; an empty list means nothing.
	mine, _ := repo.ListQAProjectSummaries(ctx, f.s, []uuid.UUID{empty.ID})
	if len(mine) != 1 || mine[0].ProjectID != empty.ID {
		t.Fatalf("restricted summaries = %+v", mine)
	}
	if none, _ := repo.ListQAProjectSummaries(ctx, f.s, []uuid.UUID{}); len(none) != 0 {
		t.Fatalf("empty restriction = %+v; want none", none)
	}

	repo.ChangeQAIssueStatus(ctx, f.s, iss.ID, "closed", nil, "")
	repo.CloseQAPlan(ctx, f.s, f.plan.ID)
	one, err := repo.QASummaryForProject(ctx, f.s, f.projectID)
	if err != nil || one.OpenIssues != 0 || one.OpenPlans != 0 || one.Cases != 2 {
		t.Fatalf("after closing = %+v, %v", one, err)
	}
	if _, err := repo.QASummaryForProject(ctx, f.s, gone.ID); err != repo.ErrNotFound {
		t.Fatalf("archived project summary = %v; want ErrNotFound", err)
	}
}

func TestQAScenarioCaseCounts_CountsLiveAndArchivedCases(t *testing.T) {
	f := newRunFixture(t, "acme-scenario-counts")
	ctx := context.Background()
	repo.ArchiveQACase(ctx, f.s, f.report.ID)
	counts, err := repo.QAScenarioCaseCounts(ctx, f.s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if c := counts[f.uat.ID]; c.Cases != 2 || c.Archived != 0 {
		t.Fatalf("uat = %+v; want 2 live cases", c)
	}
	if c := counts[f.reg.ID]; c.Cases != 2 || c.Archived != 1 {
		t.Fatalf("regression = %+v; want 2 cases, 1 of them archived", c)
	}
}
