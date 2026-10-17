package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// seedLargeScenario makes a scenario of n cases split over two groups and a
// plan over it.
func seedLargeScenario(t *testing.T, r qaRunEnv, n int) (*repo.QAScenario, *repo.QAPlan) {
	t.Helper()
	ctx := context.Background()
	sec, _ := repo.CreateQASection(ctx, r.s, r.projectID, "Bulk", nil)
	sc, _ := repo.CreateQAScenario(ctx, r.s, r.projectID, "Regresi penuh", "regression", "", nil)
	day1, _ := repo.CreateQAScenarioGroup(ctx, r.s, sc.ID, "Day 1")
	day2, _ := repo.CreateQAScenarioGroup(ctx, r.s, sc.ID, "Day 2")
	var first, second []uuid.UUID
	for i := 0; i < n; i++ {
		c, err := repo.CreateQACase(ctx, r.s, r.projectID, repo.QACaseInput{SectionID: sec.ID, Title: fmt.Sprintf("Case %03d", i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			first = append(first, c.ID)
		} else {
			second = append(second, c.ID)
		}
	}
	repo.AddQAScenarioCases(ctx, r.s, sc.ID, &day1.ID, first)
	repo.AddQAScenarioCases(ctx, r.s, sc.ID, &day2.ID, second)
	plan, _ := repo.CreateQAPlan(ctx, r.s, r.projectID, repo.QAPlanInput{Name: "Big", Phase: "regression", ScenarioIDs: []uuid.UUID{sc.ID}}, nil)
	return sc, plan
}

// Past qaCollapseAbove items a run opens with its groups closed, unless a
// filter has already narrowed it; small runs open as before.
func TestQARun_LargeRunOpensWithGroupsClosed(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qalarge-run")
	ctx := context.Background()
	_, plan := seedLargeScenario(t, r, qaCollapseAbove+1)
	big, _ := repo.CreateQARun(ctx, r.s, plan.ID, repo.QARunInput{Name: "Big"}, nil)

	_, body := r.get(t, r.member, r.base+"/runs/"+big.ID.String())
	if strings.Count(body, "<details") != 2 || strings.Contains(body, "<details open") || !strings.Contains(body, "Groups start closed") {
		t.Fatalf("large run: %d details, open=%v", strings.Count(body, "<details"), strings.Contains(body, "<details open"))
	}
	if _, body = r.get(t, r.member, r.base+"/runs/"+big.ID.String()+"?status=untested"); !strings.Contains(body, "<details open") {
		t.Fatal("a filtered large run keeps its groups closed")
	}
	small, _ := r.firstRun(t)
	if _, body = r.get(t, r.member, r.base+"/runs/"+small.ID.String()); !strings.Contains(body, "<details open") {
		t.Fatal("a small run opens with its groups closed")
	}
}

func TestQAScenario_LargeScenarioOpensWithGroupsClosed(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qalarge-scenario")
	sc, _ := seedLargeScenario(t, r, qaCollapseAbove+1)
	_, body := r.get(t, r.member, r.base+"/scenarios/"+sc.ID.String())
	if !strings.Contains(body, "<details") || strings.Contains(body, "<details open") || !strings.Contains(body, "Groups start closed") {
		t.Fatal("large scenario opens with its groups open")
	}
	list, _ := repo.ListQAScenarios(context.Background(), r.s, r.projectID, false)
	for _, s := range list {
		if s.Name == "Regression core" {
			if _, body = r.get(t, r.member, r.base+"/scenarios/"+s.ID.String()); !strings.Contains(body, "<details open") {
				t.Fatal("a small scenario opens with its groups closed")
			}
		}
	}
}
