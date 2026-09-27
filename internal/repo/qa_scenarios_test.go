package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func TestQAScenarios_GroupsOrderAndDedupe(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-scn")
	p := mustProject(t, s, "portal", nil)
	sec, _ := repo.CreateQASection(ctx, s, p.ID, "All", nil)
	login := mustQACase(t, s, p.ID, sec.ID, "Login")
	rfs := mustQACase(t, s, p.ID, sec.ID, "Create RFS")
	approve := mustQACase(t, s, p.ID, sec.ID, "Approve RFS")

	scn, err := repo.CreateQAScenario(ctx, s, p.ID, "UAT RFS flow", "uat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateQAScenario(ctx, s, p.ID, "x", "exploratory", "", nil); err != repo.ErrInvalidInput {
		t.Fatalf("unknown phase: %v; want ErrInvalidInput", err)
	}
	day2, _ := repo.CreateQAScenarioGroup(ctx, s, scn.ID, "Day 2")
	n, err := repo.AddQAScenarioCases(ctx, s, scn.ID, nil, []uuid.UUID{login.ID, rfs.ID})
	if err != nil || n != 2 {
		t.Fatalf("add = %d, %v", n, err)
	}
	n, _ = repo.AddQAScenarioCases(ctx, s, scn.ID, &day2.ID, []uuid.UUID{approve.ID, login.ID})
	if n != 1 {
		t.Fatalf("add with duplicate = %d; want 1 (login already in)", n)
	}
	entries, _ := repo.ListQAScenarioEntries(ctx, s, scn.ID)
	var titles []string
	for _, e := range entries {
		titles = append(titles, e.GroupName+":"+e.Title)
	}
	want := []string{":Login", ":Create RFS", "Day 2:Approve RFS"}
	if len(titles) != 3 || titles[0] != want[0] || titles[1] != want[1] || titles[2] != want[2] {
		t.Fatalf("entries = %v; want %v", titles, want)
	}

	// A case from another project is refused.
	p2 := mustProject(t, s, "mobile", nil)
	sec2, _ := repo.CreateQASection(ctx, s, p2.ID, "All", nil)
	foreign := mustQACase(t, s, p2.ID, sec2.ID, "Foreign")
	if _, err := repo.AddQAScenarioCases(ctx, s, scn.ID, nil, []uuid.UUID{foreign.ID}); err != repo.ErrInvalidInput {
		t.Fatalf("foreign case: %v; want ErrInvalidInput", err)
	}

	// Reorder the ungrouped items.
	if err := repo.ReorderQAScenario(ctx, s, scn.ID, []uuid.UUID{entries[1].ItemID, entries[0].ItemID}); err != nil {
		t.Fatal(err)
	}
	entries, _ = repo.ListQAScenarioEntries(ctx, s, scn.ID)
	if entries[0].Title != "Create RFS" {
		t.Fatalf("after reorder first = %q; want Create RFS", entries[0].Title)
	}
}

func TestQAScenarios_GroupMustBelongToTheScenario(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-scg")
	p := mustProject(t, s, "portal", nil)
	sec, _ := repo.CreateQASection(ctx, s, p.ID, "All", nil)
	c := mustQACase(t, s, p.ID, sec.ID, "Login")
	a, _ := repo.CreateQAScenario(ctx, s, p.ID, "A", "uat", "", nil)
	b, _ := repo.CreateQAScenario(ctx, s, p.ID, "B", "uat", "", nil)
	gb, _ := repo.CreateQAScenarioGroup(ctx, s, b.ID, "Day 1")
	if _, err := repo.AddQAScenarioCases(ctx, s, a.ID, &gb.ID, []uuid.UUID{c.ID}); err != repo.ErrInvalidInput {
		t.Fatalf("group of another scenario: %v; want ErrInvalidInput", err)
	}
}

func TestQAScenarios_ListGroupsInOrder(t *testing.T) {
	f := newRunFixture(t, "acme-scenario-groups")
	ctx := context.Background()
	repo.CreateQAScenarioGroup(ctx, f.s, f.uat.ID, "Day 3")
	groups, err := repo.ListQAScenarioGroups(ctx, f.s, f.uat.ID)
	if err != nil || len(groups) != 2 || groups[0].Name != "Day 2" || groups[1].Name != "Day 3" {
		t.Fatalf("groups = %+v, %v; want Day 2 then the empty Day 3", groups, err)
	}
}

func TestQAScenarios_RenameReorderAndRegroup(t *testing.T) {
	f := newRunFixture(t, "acme-scenario-edit")
	ctx := context.Background()
	day2 := mustGroup(t, f, "Day 2")
	day3, _ := repo.CreateQAScenarioGroup(ctx, f.s, f.uat.ID, "Day 3")
	other, _ := repo.CreateQAScenarioGroup(ctx, f.s, f.reg.ID, "Elsewhere")

	if err := repo.RenameQAScenarioGroup(ctx, f.s, f.uat.ID, day3.ID, "Day 1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RenameQAScenarioGroup(ctx, f.s, f.uat.ID, other.ID, "Hijack"); err != repo.ErrNotFound {
		t.Fatalf("rename another scenario's group = %v; want ErrNotFound", err)
	}
	if err := repo.ReorderQAScenarioGroups(ctx, f.s, f.uat.ID, []uuid.UUID{day3.ID, day2}); err != nil {
		t.Fatal(err)
	}
	groups, _ := repo.ListQAScenarioGroups(ctx, f.s, f.uat.ID)
	if groups[0].Name != "Day 1" || groups[1].ID != day2 {
		t.Fatalf("groups = %+v; want Day 1 first", groups)
	}

	entries, _ := repo.ListQAScenarioEntries(ctx, f.s, f.uat.ID)
	var loginItem uuid.UUID
	for _, e := range entries {
		if e.CaseID == f.login.ID {
			loginItem = e.ItemID
		}
	}
	regEntries, _ := repo.ListQAScenarioEntries(ctx, f.s, f.reg.ID)
	n, err := repo.SetQAScenarioItemsGroup(ctx, f.s, f.uat.ID, []uuid.UUID{loginItem, regEntries[0].ItemID}, &day3.ID)
	if err != nil || n != 1 {
		t.Fatalf("regroup = %d, %v; want 1 (the other scenario's item is skipped)", n, err)
	}
	if _, err := repo.SetQAScenarioItemsGroup(ctx, f.s, f.uat.ID, []uuid.UUID{loginItem}, &other.ID); err != repo.ErrNotFound {
		t.Fatalf("regroup into another scenario's group = %v; want ErrNotFound", err)
	}
	entries, _ = repo.ListQAScenarioEntries(ctx, f.s, f.uat.ID)
	if entries[0].CaseID != f.login.ID || entries[0].GroupName != "Day 1" {
		t.Fatalf("entries = %+v; want Login first, in Day 1", entries)
	}
}

func mustGroup(t *testing.T, f runFixture, name string) uuid.UUID {
	t.Helper()
	groups, _ := repo.ListQAScenarioGroups(context.Background(), f.s, f.uat.ID)
	for _, g := range groups {
		if g.Name == name {
			return g.ID
		}
	}
	t.Fatalf("no group %q", name)
	return uuid.Nil
}

func TestCreateQAScenarioFromRun_KeepsOrderAndGroups(t *testing.T) {
	f := newRunFixture(t, "acme-scenario-from-run")
	ctx := context.Background()
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1", ScenarioIDs: []uuid.UUID{f.uat.ID}}, nil)
	repo.ArchiveQACase(ctx, f.s, f.login.ID) // archived since the run: left out

	sc, err := repo.CreateQAScenarioFromRun(ctx, f.s, run.ID, "UAT RFS again", "uat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sc.ProjectID != f.projectID || sc.Phase != "uat" {
		t.Fatalf("scenario = %+v", sc)
	}
	entries, _ := repo.ListQAScenarioEntries(ctx, f.s, sc.ID)
	if len(entries) != 1 || entries[0].CaseID != f.rfs.ID || entries[0].GroupName != "Day 2" {
		t.Fatalf("entries = %+v; want Create RFS in Day 2 (Login is archived)", entries)
	}
	if _, err := repo.CreateQAScenarioFromRun(ctx, f.s, run.ID, " ", "uat", nil); err != repo.ErrInvalidInput {
		t.Fatalf("blank name = %v", err)
	}
}
