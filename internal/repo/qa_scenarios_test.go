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
