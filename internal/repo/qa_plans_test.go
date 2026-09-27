package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAPlans_DuplicateCopiesScopeNotDates(t *testing.T) {
	f := newRunFixture(t, "acme-dup")
	ctx := context.Background()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 14)
	if err := repo.UpdateQAPlan(ctx, f.s, f.plan.ID, repo.QAPlanInput{
		Name: f.plan.Name, Phase: "uat", Environment: "UAT", Build: "1.4.0",
		StartsOn: &start, EndsOn: &end, ScenarioIDs: []uuid.UUID{f.uat.ID},
	}); err != nil {
		t.Fatal(err)
	}
	dup, err := repo.DuplicateQAPlan(ctx, f.s, f.plan.ID, "UAT October", nil)
	if err != nil {
		t.Fatal(err)
	}
	if dup.Phase != "uat" || dup.Environment != "UAT" || dup.Build != "1.4.0" || dup.StartsOn != nil || dup.ClosedAt != nil {
		t.Fatalf("dup = %+v", dup)
	}
	ids, _ := repo.ListQAPlanScenarioIDs(ctx, f.s, dup.ID)
	if len(ids) != 1 || ids[0] != f.uat.ID {
		t.Fatalf("dup scenarios = %v; want [uat]", ids)
	}
	bad := end.AddDate(0, 0, -30)
	if _, err := repo.CreateQAPlan(ctx, f.s, f.projectID, repo.QAPlanInput{Name: "x", Phase: "uat", StartsOn: &start, EndsOn: &bad}, nil); err != repo.ErrInvalidInput {
		t.Fatalf("end before start: %v; want ErrInvalidInput", err)
	}
}
