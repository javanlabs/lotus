package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAShareLinks_ActiveUntilRevokedOrExpired(t *testing.T) {
	f := newRunFixture(t, "acme-share")
	ctx := context.Background()
	link, err := repo.CreateQAShareLink(ctx, f.s, f.plan.ID, "hash-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(link.ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("expires in %v; want 30 days", d)
	}
	if got, err := repo.QAShareLinkByHash(ctx, f.s, "hash-a"); err != nil || got.PlanID != f.plan.ID {
		t.Fatalf("by hash = %+v, %v", got, err)
	}
	expired, _ := repo.CreateQAShareLink(ctx, f.s, f.plan.ID, "hash-b", nil)
	if err := f.s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.Exec(`UPDATE qa_share_links SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QAShareLinkByHash(ctx, f.s, "hash-b"); err != repo.ErrNotFound {
		t.Errorf("expired: %v; want ErrNotFound", err)
	}
	if err := repo.RevokeQAShareLink(ctx, f.s, f.plan.ID, link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QAShareLinkByHash(ctx, f.s, "hash-a"); err != repo.ErrNotFound {
		t.Errorf("revoked: %v; want ErrNotFound", err)
	}
	if err := repo.RevokeQAShareLink(ctx, f.s, f.plan.ID, link.ID); err != repo.ErrNotFound {
		t.Errorf("revoke twice: %v; want ErrNotFound", err)
	}
	other, _ := repo.CreateQAPlan(ctx, f.s, f.projectID, repo.QAPlanInput{Name: "Other", Phase: "uat"}, nil)
	third, _ := repo.CreateQAShareLink(ctx, f.s, f.plan.ID, "hash-c", nil)
	if err := repo.RevokeQAShareLink(ctx, f.s, other.ID, third.ID); err != repo.ErrNotFound {
		t.Errorf("revoke through another plan: %v; want ErrNotFound", err)
	}
	active, _ := repo.ListQAShareLinks(ctx, f.s, f.plan.ID)
	if len(active) != 1 || active[0].ID != third.ID {
		t.Fatalf("active = %+v; want only the third link", active)
	}
}

func TestQAPlanScenarioProgress_LatestStatusPerCase(t *testing.T) {
	f := newRunFixture(t, "acme-scenario-progress")
	ctx := context.Background()
	run, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "Cycle 1", Distribution: repo.DistributeNone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	for _, it := range items {
		status := map[string]string{"Login": "failed", "Create RFS": "passed"}[it.Title]
		if status == "" {
			continue
		}
		if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: it.ID, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.QAPlanScenarioProgress(ctx, f.s, f.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p := got[f.uat.ID]; p.Total != 2 || p.Failed != 1 || p.Passed != 1 {
		t.Errorf("UAT RFS = %+v; want 2 cases, 1 failed, 1 passed", p)
	}
	if p := got[f.reg.ID]; p.Total != 2 || p.Failed != 1 || p.Untested != 1 {
		t.Errorf("Regression core = %+v; want 2 cases, 1 failed, 1 untested", p)
	}
}
