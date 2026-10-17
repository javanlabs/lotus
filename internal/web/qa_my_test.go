package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAMyWork_ListsOpenUntestedItemsAndCountsThem(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qamy")
	ctx := context.Background()
	run, items := r.firstRun(t) // 2 items
	repo.AssignQARunItems(ctx, r.s, run.ID, []uuid.UUID{items[0].ID, items[1].ID}, &r.memberID)

	// A passed item, an item in a closed run and one in a closed plan are not work.
	second, _ := repo.CreateQARun(ctx, r.s, r.plan.ID, repo.QARunInput{Name: "Cycle 2", Testers: []uuid.UUID{r.memberID}, Distribution: repo.DistributeRoundRobin}, nil)
	repo.CloseQARun(ctx, r.s, second.ID)
	repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[1].ID, Status: "passed"})
	otherPlan, _ := repo.DuplicateQAPlan(ctx, r.s, r.plan.ID, "Regression November", nil)
	repo.CreateQARun(ctx, r.s, otherPlan.ID, repo.QARunInput{Name: "Cycle 1", Testers: []uuid.UUID{r.memberID}, Distribution: repo.DistributeRoundRobin}, nil)
	repo.CloseQAPlan(ctx, r.s, otherPlan.ID)

	status, body := r.get(t, r.member, "/qa/my")
	if status != http.StatusOK || strings.Count(body, "data-my-item") != 1 || !strings.Contains(body, "Portal") ||
		!strings.Contains(body, "Regression October") || !strings.Contains(body, `href="`+r.base+"/runs/"+run.ID.String()+`?who=mine"`) {
		t.Fatalf("my work = %d:\n%s", status, body)
	}
	mine, _ := repo.ListMyQAItems(ctx, r.s, r.memberID, nil)
	if len(mine) != 1 || !strings.Contains(body, `data-qa-my-count="1"`) {
		t.Fatalf("sidebar count missing or wrong (repo says %d)", len(mine))
	}
	// Any page with the sidebar carries the count.
	if _, body = r.get(t, r.member, "/projects"); !strings.Contains(body, `data-qa-my-count="1"`) {
		t.Fatal("the sidebar count is only on QA pages")
	}
}

func TestQAMyWork_GuestSeesOnlyItsProjects(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qamy-guest")
	ctx := context.Background()
	run, items := r.firstRun(t)
	repo.AssignQARunItems(ctx, r.s, run.ID, []uuid.UUID{items[0].ID}, &r.guestID)
	_, body := r.get(t, r.guest, "/qa/my")
	if strings.Count(body, "data-my-item") != 1 {
		t.Fatalf("guest my work:\n%s", body)
	}
	repo.RemoveProjectMember(ctx, r.s, r.projectID, r.guestID)
	if _, body = r.get(t, r.guest, "/qa/my"); strings.Contains(body, "data-my-item") {
		t.Fatal("guest removed from the project still sees its items")
	}
}
