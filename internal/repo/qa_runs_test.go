package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

type runFixture struct {
	db                 *sqlx.DB
	s                  tenant.Scope
	projectID          uuid.UUID
	login, rfs, report *repo.QACase
	uat, reg           *repo.QAScenario
	plan               *repo.QAPlan
}

func newRunFixture(t *testing.T, sub string) runFixture {
	t.Helper()
	ctx := context.Background()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, sub)
	p := mustProject(t, s, "portal", nil)
	sec, _ := repo.CreateQASection(ctx, s, p.ID, "All", nil)
	f := runFixture{db: db, s: s, projectID: p.ID}
	f.login = mustQACase(t, s, p.ID, sec.ID, "Login")
	f.rfs = mustQACase(t, s, p.ID, sec.ID, "Create RFS")
	f.report = mustQACase(t, s, p.ID, sec.ID, "Report")

	f.uat, _ = repo.CreateQAScenario(ctx, s, p.ID, "UAT RFS", "uat", "", nil)
	g, _ := repo.CreateQAScenarioGroup(ctx, s, f.uat.ID, "Day 2")
	repo.AddQAScenarioCases(ctx, s, f.uat.ID, nil, []uuid.UUID{f.login.ID})
	repo.AddQAScenarioCases(ctx, s, f.uat.ID, &g.ID, []uuid.UUID{f.rfs.ID})
	f.reg, _ = repo.CreateQAScenario(ctx, s, p.ID, "Regression core", "regression", "", nil)
	repo.AddQAScenarioCases(ctx, s, f.reg.ID, nil, []uuid.UUID{f.login.ID, f.report.ID})

	var err error
	f.plan, err = repo.CreateQAPlan(ctx, s, p.ID, repo.QAPlanInput{
		Name: "UAT September", Phase: "uat", Environment: "UAT", ScenarioIDs: []uuid.UUID{f.uat.ID, f.reg.ID},
	}, nil)
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return f
}

func TestQARuns_SnapshotDedupeAndGroupNames(t *testing.T) {
	f := newRunFixture(t, "acme-run")
	ctx := context.Background()
	run, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "Cycle 1", Distribution: repo.DistributeNone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Cycle != 1 {
		t.Errorf("cycle = %d; want 1", run.Cycle)
	}
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	var got []string
	for _, it := range items {
		got = append(got, it.GroupName+"|"+it.Title)
	}
	// Scenario order is by name: "Regression core" before "UAT RFS". Login
	// appears in both and is kept once, at its first position.
	want := []string{"Regression core|Login", "Regression core|Report", "UAT RFS / Day 2|Create RFS"}
	if len(got) != len(want) {
		t.Fatalf("items = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v; want %v", got, want)
		}
	}

	// Editing a case does not change the run's snapshot.
	sec := f.login.SectionID
	repo.UpdateQACase(ctx, f.s, f.login.ID, repo.QACaseInput{SectionID: sec, Title: "Login with SSO", Steps: f.login.Steps})
	items, _ = repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	if items[0].Title != "Login" {
		t.Fatalf("snapshot changed to %q", items[0].Title)
	}
}

func TestQARuns_Distribution(t *testing.T) {
	f := newRunFixture(t, "acme-dist")
	ctx := context.Background()
	a := mustUser(t, f.db, "a@acme.example")
	b := mustUser(t, f.db, "b@acme.example")

	rr, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{
		Name: "RR", Testers: []uuid.UUID{a, b}, Distribution: repo.DistributeRoundRobin,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListQARunItems(ctx, f.s, rr.ID, repo.QAItemFilter{})
	if *items[0].AssigneeID != a || *items[1].AssigneeID != b || *items[2].AssigneeID != a {
		t.Errorf("round robin assignees wrong: %v %v %v", *items[0].AssigneeID, *items[1].AssigneeID, *items[2].AssigneeID)
	}
	if rr.Cycle != 1 {
		t.Errorf("first run cycle = %d", rr.Cycle)
	}

	bg, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{
		Name: "BG", Testers: []uuid.UUID{a, b}, Distribution: repo.DistributeByGroup,
	}, nil)
	items, _ = repo.ListQARunItems(ctx, f.s, bg.ID, repo.QAItemFilter{})
	// Two groups: "Regression core" (2 items) → a, "UAT RFS / Day 2" → b.
	if *items[0].AssigneeID != a || *items[1].AssigneeID != a || *items[2].AssigneeID != b {
		t.Errorf("by-group assignees wrong")
	}
	if bg.Cycle != 2 {
		t.Errorf("second run cycle = %d; want 2", bg.Cycle)
	}
	if _, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "x", Distribution: repo.DistributeRoundRobin}, nil); err != repo.ErrInvalidInput {
		t.Errorf("round robin without testers: %v; want ErrInvalidInput", err)
	}
}

func TestQARuns_RetestTakesFailedAndBlockedWithCurrentText(t *testing.T) {
	f := newRunFixture(t, "acme-retest")
	ctx := context.Background()
	tester := mustUser(t, f.db, "t@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "Cycle 1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", CreatedBy: &tester})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[1].ID, Status: "passed", CreatedBy: &tester})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[2].ID, Status: "blocked", CreatedBy: &tester})

	repo.UpdateQACase(ctx, f.s, f.login.ID, repo.QACaseInput{SectionID: f.login.SectionID, Title: "Login (fixed)", Steps: f.login.Steps})
	re, err := repo.CreateQARetest(ctx, f.s, run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if re.RetestOf == nil || *re.RetestOf != run.ID || re.Cycle != 2 || re.Name != "Cycle 1 — retest" {
		t.Fatalf("retest run = %+v", re)
	}
	got, _ := repo.ListQARunItems(ctx, f.s, re.ID, repo.QAItemFilter{})
	if len(got) != 2 || got[0].Title != "Login (fixed)" || got[1].Title != "Create RFS" {
		t.Fatalf("retest items = %+v", got)
	}
	if got[0].AssigneeID == nil || *got[0].AssigneeID != tester {
		t.Fatalf("retest item not assigned to the original executor")
	}
}

func TestQARuns_ClosedPlanRefusesRuns(t *testing.T) {
	f := newRunFixture(t, "acme-closed")
	ctx := context.Background()
	if err := repo.CloseQAPlan(ctx, f.s, f.plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "late"}, nil); err != repo.ErrQAClosed {
		t.Fatalf("run on closed plan: %v; want ErrQAClosed", err)
	}
}
