package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

// A run started while its plan is being closed must not land in the closed
// plan: the plan row is read under lock, so the create waits and then sees
// closed_at.
func TestQARuns_CreateWaitsForAConcurrentPlanClose(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-close-race")
	ctx := context.Background()
	done := make(chan error, 1)
	err := f.s.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`SELECT 1 FROM qa_plans WHERE id = $1 FOR UPDATE`, f.plan.ID); err != nil {
			return err
		}
		go func() {
			_, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
			done <- err
		}()
		time.Sleep(200 * time.Millisecond)
		_, err := tx.Exec(`UPDATE qa_plans SET closed_at = now() WHERE id = $1`, f.plan.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != repo.ErrQAClosed {
		t.Fatalf("run created during the close = %v; want ErrQAClosed", err)
	}
}

// Likewise a result recorded while its run is being closed.
func TestQAItems_ResultWaitsForAConcurrentRunClose(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-result-race")
	ctx := context.Background()
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	done := make(chan error, 1)
	err := f.s.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`SELECT 1 FROM qa_runs WHERE id = $1 FOR UPDATE`, run.ID); err != nil {
			return err
		}
		go func() {
			_, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "passed"})
			done <- err
		}()
		time.Sleep(200 * time.Millisecond)
		_, err := tx.Exec(`UPDATE qa_runs SET closed_at = now() WHERE id = $1`, run.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != repo.ErrQAClosed {
		t.Fatalf("result recorded during the close = %v; want ErrQAClosed", err)
	}
}

func TestQARuns_RetestNameStaysWithinLimitAndDoesNotStack(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-retest-name")
	ctx := context.Background()
	long := strings.Repeat("r", 195)
	run, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: long}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed"})
	retest, err := repo.CreateQARetest(ctx, f.s, run.ID, nil)
	if err != nil {
		t.Fatalf("retest of a long name = %v", err)
	}
	if len([]rune(retest.Name)) > repo.MaxQANameLength || !strings.HasSuffix(retest.Name, " — retest") {
		t.Fatalf("retest name = %q", retest.Name)
	}

	short, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "Cycle 1"}, nil)
	its, _ := repo.ListQARunItems(ctx, f.s, short.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: its[0].ID, Status: "failed"})
	r1, _ := repo.CreateQARetest(ctx, f.s, short.ID, nil)
	r1items, _ := repo.ListQARunItems(ctx, f.s, r1.ID, repo.QAItemFilter{})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: r1items[0].ID, Status: "blocked"})
	r2, err := repo.CreateQARetest(ctx, f.s, r1.ID, nil)
	if err != nil || r2.Name != "Cycle 1 — retest" {
		t.Fatalf("retest of a retest = %v, %q; want %q", err, r2.Name, "Cycle 1 — retest")
	}
}
