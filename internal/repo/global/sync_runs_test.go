package global_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func seedOrgConnBinding(t *testing.T, db *sqlx.DB, subdomain string) (orgID, connID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxGlobalTest(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		var componentID uuid.UUID
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'gl', 'https://gitlab.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'gitlab', 'group/repo') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, connID, bindingID
}

func withOrgTxGlobalTest(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestEnqueueRun_ManualTriggerDedupesAgainstInFlightRun(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedOrgConnBinding(t, db, "acme")

	id1, inserted1, err := global.EnqueueRun(context.Background(), db, global.NewSyncRun{
		OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if !inserted1 {
		t.Fatal("expected the first enqueue to insert a new run")
	}

	id2, inserted2, err := global.EnqueueRun(context.Background(), db, global.NewSyncRun{
		OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue 2 (manual): %v", err)
	}
	if inserted2 {
		t.Fatal("expected the manual trigger to dedupe against the in-flight scheduled run, not insert a second one")
	}
	if id2 != 0 {
		t.Fatalf("expected id 0 for a deduped enqueue, got %d", id2)
	}
	_ = id1
}

func TestClaimRun_TwoWorkersClaimDifferentRuns(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, binding1 := seedOrgConnBinding(t, db, "acme")
	_, _, binding2 := seedOrgConnBindingSameOrg(t, db, orgID, connID)

	ctx := context.Background()
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: binding1, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: binding2, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	run1, err := global.ClaimRun(ctx, db, "worker-1", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	if run1 == nil {
		t.Fatal("expected worker 1 to claim a run")
	}
	run2, err := global.ClaimRun(ctx, db, "worker-2", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if run2 == nil {
		t.Fatal("expected worker 2 to claim a run")
	}
	if run1.ID == run2.ID {
		t.Fatal("two concurrent claims must not return the same run")
	}
	if run1.Status != "leased" || run2.Status != "leased" {
		t.Fatalf("claimed runs should be status=leased, got %q and %q", run1.Status, run2.Status)
	}

	run3, err := global.ClaimRun(ctx, db, "worker-3", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim 3: %v", err)
	}
	if run3 != nil {
		t.Fatal("expected no third claimable run")
	}
}

func seedOrgConnBindingSameOrg(t *testing.T, db *sqlx.DB, orgID, connID uuid.UUID) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	var bindingID uuid.UUID
	withOrgTxGlobalTest(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p2', 'P2') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		var componentID uuid.UUID
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c2', 'C2', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'gitlab', 'group/repo2') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, connID, bindingID
}

func TestClaimRun_SkipsRateLimitedConnection(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedOrgConnBinding(t, db, "acme")
	ctx := context.Background()

	if err := global.SetConnectionRateLimited(context.Background(), db, connID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("set rate_limited_until: %v", err)
	}
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	run, err := global.ClaimRun(ctx, db, "worker-1", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if run != nil {
		t.Fatal("expected a rate-limited connection's run to not be claimable")
	}
}

func TestClaimRun_ExcludesOrgsAtCapacity(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedOrgConnBinding(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	run, err := global.ClaimRun(ctx, db, "worker-1", []uuid.UUID{orgID}, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if run != nil {
		t.Fatal("expected the excluded org's run to not be claimable")
	}
}

func TestReapExpiredLeases_RequeuesDeadWorkerRuns(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedOrgConnBinding(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Claim with a lease that's already expired, simulating a worker that
	// died mid-sync.
	run, err := global.ClaimRun(ctx, db, "worker-doomed", nil, -1*time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if run == nil {
		t.Fatal("expected a claim")
	}

	n, err := global.ReapExpiredLeases(ctx, db)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d runs, want 1", n)
	}

	requeued, err := global.ClaimRun(ctx, db, "worker-2", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if requeued == nil {
		t.Fatal("expected the reaped run to be claimable again")
	}
	if requeued.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1 (incremented by the reaper)", requeued.Attempt)
	}
}

func TestOrgsAtCapacity_ReflectsQuotaAndReleasesOnFinish(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, binding1 := seedOrgConnBinding(t, db, "acme")
	_, _, binding2 := seedOrgConnBindingSameOrg(t, db, orgID, connID)
	ctx := context.Background()

	if _, err := db.Exec(`UPDATE organizations SET sync_concurrency_quota = 1 WHERE id = $1`, orgID); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: binding1, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: binding2, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	run, err := global.ClaimRun(ctx, db, "worker-1", nil, 10*time.Minute)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}

	atCapacity, err := global.OrgsAtCapacity(ctx, db)
	if err != nil {
		t.Fatalf("OrgsAtCapacity: %v", err)
	}
	if len(atCapacity) != 1 || atCapacity[0] != orgID {
		t.Fatalf("atCapacity = %v, want [%s] (quota=1, one run leased)", atCapacity, orgID)
	}

	// With the org excluded, its second (still-queued) run isn't claimable.
	second, err := global.ClaimRun(ctx, db, "worker-2", atCapacity, 10*time.Minute)
	if err != nil {
		t.Fatalf("claim while at capacity: %v", err)
	}
	if second != nil {
		t.Fatal("expected no claimable run while the org is at capacity")
	}

	// Finishing the first run frees capacity.
	if err := global.FinishRun(ctx, db, run.ID, global.FinishRunParams{Status: "ok"}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	afterFinish, err := global.OrgsAtCapacity(ctx, db)
	if err != nil {
		t.Fatalf("OrgsAtCapacity after finish: %v", err)
	}
	if len(afterFinish) != 0 {
		t.Fatalf("atCapacity after finishing = %v, want empty", afterFinish)
	}
}

func TestFinishRun_SetsTerminalStatus(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedOrgConnBinding(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	run, err := global.ClaimRun(ctx, db, "worker-1", nil, 10*time.Minute)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := global.MarkRunStarted(ctx, db, run.ID); err != nil {
		t.Fatalf("mark started: %v", err)
	}
	if err := global.FinishRun(ctx, db, run.ID, global.FinishRunParams{Status: "ok"}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// The binding is free again — a new run can be enqueued and claimed.
	if _, inserted, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "scheduled", ScheduledFor: time.Now()}); err != nil || !inserted {
		t.Fatalf("expected a new run to be enqueueable once the previous one finished: inserted=%v err=%v", inserted, err)
	}
}
