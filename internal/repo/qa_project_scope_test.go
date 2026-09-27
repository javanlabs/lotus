package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// A second project in the same organization: RLS lets its rows through, so
// every project boundary below has to be checked by the repo itself.
func otherProject(t *testing.T, f runFixture) (sectionID, planID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	p := mustProject(t, f.s, "billing", nil)
	sec, err := repo.CreateQASection(ctx, f.s, p.ID, "Billing", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := repo.CreateQAPlan(ctx, f.s, p.ID, repo.QAPlanInput{Name: "Billing SIT", Phase: "sit"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sec.ID, plan.ID
}

func TestQAIssues_PlanAndSectionMustBeInTheProject(t *testing.T) {
	f := newRunFixture(t, "acme-iss-scope")
	ctx := context.Background()
	otherSection, otherPlan := otherProject(t, f)

	if _, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{
		Title: "x", Severity: "minor", Source: "client", PlanID: &otherPlan,
	}, nil, nil); err != repo.ErrNotFound {
		t.Fatalf("create with another project's plan = %v; want ErrNotFound", err)
	}
	if _, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{
		Title: "x", Severity: "minor", Source: "client", SectionID: &otherSection,
	}, nil, nil); err != repo.ErrNotFound {
		t.Fatalf("create with another project's section = %v; want ErrNotFound", err)
	}

	iss, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{
		Title: "x", Severity: "minor", Source: "internal", PlanID: &f.plan.ID,
	}, nil, nil)
	if err != nil {
		t.Fatalf("create with own plan: %v", err)
	}
	in := repo.QAIssueInput{Title: "x", Severity: "minor", Source: "internal", PlanID: &otherPlan}
	if err := repo.UpdateQAIssue(ctx, f.s, iss.ID, in); err != repo.ErrNotFound {
		t.Fatalf("update to another project's plan = %v; want ErrNotFound", err)
	}
	in = repo.QAIssueInput{Title: "x", Severity: "minor", Source: "internal", SectionID: &otherSection}
	if err := repo.UpdateQAIssue(ctx, f.s, iss.ID, in); err != repo.ErrNotFound {
		t.Fatalf("update to another project's section = %v; want ErrNotFound", err)
	}
}

func TestQAMyWork_GuestWithoutProjectsSeesNothing(t *testing.T) {
	f := newRunFixture(t, "acme-mywork-guest")
	ctx := context.Background()
	guest := mustUser(t, f.db, "client@bank.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	if _, err := repo.AssignQARunItems(ctx, f.s, run.ID, []uuid.UUID{items[0].ID}, &guest); err != nil {
		t.Fatal(err)
	}

	// The guest was removed from every project; its assignments remain.
	ids, err := repo.ListProjectIDsForMember(ctx, f.s, guest)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := repo.ListMyQAItems(ctx, f.s, guest, ids)
	if err != nil || len(mine) != 0 {
		t.Fatalf("guest on no projects sees %d items (%v); want 0", len(mine), err)
	}
}

func TestQAProjectOf_ResolvesEveryKindToItsProject(t *testing.T) {
	f := newRunFixture(t, "acme-project-of")
	ctx := context.Background()
	qa := mustUser(t, f.db, "qa@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", CreatedBy: &qa}); err != nil {
		t.Fatal(err)
	}
	results, _ := repo.ListQAResults(ctx, f.s, items[0].ID)
	iss, _ := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{Title: "x", Severity: "minor", Source: "internal"}, &qa, &items[0].ID)
	repo.CommentQAIssue(ctx, f.s, iss.ID, "still broken", &qa)
	events, _ := repo.ListQAIssueEvents(ctx, f.s, iss.ID)
	groups, _ := repo.ListQAScenarioEntries(ctx, f.s, f.uat.ID)

	cases := map[repo.QAKind]uuid.UUID{
		repo.QAKindSection:  f.login.SectionID,
		repo.QAKindCase:     f.login.ID,
		repo.QAKindScenario: f.uat.ID,
		repo.QAKindPlan:     f.plan.ID,
		repo.QAKindRun:      run.ID,
		repo.QAKindItem:     items[0].ID,
		repo.QAKindResult:   results[0].ID,
		repo.QAKindIssue:    iss.ID,
		repo.QAKindEvent:    events[0].ID,
	}
	for _, e := range groups {
		if e.GroupID != nil {
			cases[repo.QAKindScenarioGroup] = *e.GroupID
		}
	}
	if len(cases) != 10 {
		t.Fatalf("fixture has no scenario group: %+v", groups)
	}
	for kind, id := range cases {
		got, err := repo.QAProjectOf(ctx, f.s, kind, id)
		if err != nil || got != f.projectID {
			t.Errorf("QAProjectOf(%s) = %v, %v; want %v", kind, got, err, f.projectID)
		}
		if _, err := repo.QAProjectOf(ctx, f.s, kind, uuid.New()); err != repo.ErrNotFound {
			t.Errorf("QAProjectOf(%s, unknown id) = %v; want ErrNotFound", kind, err)
		}
	}
	if _, err := repo.QAProjectOf(ctx, f.s, "bogus", f.plan.ID); err != repo.ErrInvalidInput {
		t.Fatalf("unknown kind = %v; want ErrInvalidInput", err)
	}
}
