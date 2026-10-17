package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxSchedTest(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
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

func seedFixture(t *testing.T, db *sqlx.DB, subdomain string) (orgID, connID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
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

func numFact(key string, v float64) collector.Fact {
	return collector.Fact{Key: key, ValueNum: &v}
}

func TestPool_SuccessfulRunUpdatesBindingSchedule(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{Headline: collector.Headline{Status: collector.StatusPass}, Facts: []collector.Fact{numFact("gitlab.commits_30d", 5)}}, nil
	}}
	claimed, err := pool.ClaimAndRun(ctx)
	if err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	if !claimed {
		t.Fatal("expected a run to be claimed")
	}

	scope := tenant.FromRun(db, orgID)
	binding, err := repo.BindingByID(ctx, scope, bindingID)
	if err != nil {
		t.Fatalf("BindingByID: %v", err)
	}
	if binding.NextSyncAt == nil {
		t.Fatal("expected next_sync_at to be set after a successful run")
	}
	if binding.LastStatus == nil || *binding.LastStatus != "ok" {
		t.Fatalf("last_status = %v, want ok", binding.LastStatus)
	}
}

func TestPool_PartialResultStillStoresAndMarksPartial(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{
			Headline: collector.Headline{Status: collector.StatusFail},
			Facts:    []collector.Fact{numFact("gitlab.commits_30d", 5)},
			Warnings: []collector.Warning{{Step: "languages", Err: "404"}},
		}, nil
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}

	scope := tenant.FromRun(db, orgID)
	binding, err := repo.BindingByID(ctx, scope, bindingID)
	if err != nil {
		t.Fatalf("BindingByID: %v", err)
	}
	if binding.LastStatus == nil || *binding.LastStatus != "partial" {
		t.Fatalf("last_status = %v, want partial (a Result with Warnings must not be treated as a hard failure)", binding.LastStatus)
	}
}

func TestPool_AuthErrorFreezesEveryBindingOnConnection(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, binding1 := seedFixture(t, db, "acme")
	ctx := context.Background()

	// A second binding on the same connection.
	var binding2 uuid.UUID
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p2', 'P2') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		var componentID uuid.UUID
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c2', 'C2', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&binding2, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'gitlab', 'group/repo2') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding 2: %v", err)
		}
	})

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: binding1, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{}, collector.ErrAuth(errors.New("401 unauthorized"))
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}

	scope := tenant.FromRun(db, orgID)
	b1, err := repo.BindingByID(ctx, scope, binding1)
	if err != nil {
		t.Fatalf("BindingByID 1: %v", err)
	}
	b2, err := repo.BindingByID(ctx, scope, binding2)
	if err != nil {
		t.Fatalf("BindingByID 2: %v", err)
	}
	if b1.Enabled || b2.Enabled {
		t.Fatalf("expected both bindings on the connection to be disabled after an auth error, got b1.Enabled=%v b2.Enabled=%v", b1.Enabled, b2.Enabled)
	}

	conn, err := repo.ConnectionByID(ctx, scope, connID)
	if err != nil {
		t.Fatalf("ConnectionByID: %v", err)
	}
	if conn.Status != "auth_failed" {
		t.Fatalf("connection status = %q, want auth_failed", conn.Status)
	}
}

func TestPool_RateLimitedSetsConnectionStateAndReschedules(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme")
	ctx := context.Background()

	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{}, collector.ErrRateLimited(errors.New("429"), time.Hour)
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}

	state, err := global.GetConnectionSyncState(ctx, db, connID)
	if err != nil {
		t.Fatalf("GetConnectionSyncState: %v", err)
	}
	if state.RateLimitedUntil == nil {
		t.Fatal("expected rate_limited_until to be set")
	}

	// The run should have been re-enqueued for after the window — the
	// binding is in flight again (deduping a fresh manual trigger).
	_, inserted, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()})
	if err != nil {
		t.Fatalf("enqueue after rate limit: %v", err)
	}
	if inserted {
		t.Fatal("expected the rate-limited run's re-enqueue to already be in flight")
	}
}

func TestPool_NoClaimableRunReturnsFalseNotError(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		t.Fatal("Execute should not be called when nothing is claimable")
		return collector.Result{}, nil
	}}
	claimed, err := pool.ClaimAndRun(context.Background())
	if err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	if claimed {
		t.Fatal("expected claimed=false on an empty queue")
	}
}

// A binding with its own sync_interval_seconds (a daily repository
// scan) is next due a day out, not at the scheduler's 15-minute default.
func TestPool_BindingIntervalSetsNextSync(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme-interval")
	ctx := context.Background()
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET sync_interval_seconds = 86400 WHERE id = $1`, bindingID); err != nil {
			t.Fatalf("set interval: %v", err)
		}
	})
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{Facts: []collector.Fact{numFact("gitlab.commits_30d", 5)}}, nil
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	binding, err := repo.BindingByID(ctx, tenant.FromRun(db, orgID), bindingID)
	if err != nil {
		t.Fatalf("BindingByID: %v", err)
	}
	if d := time.Until(*binding.NextSyncAt); d < 20*time.Hour || d > 28*time.Hour {
		t.Fatalf("next sync in %v, want about a day", d)
	}
}

// A collect that outlasts its lease keeps it: the worker renews the
// lease while Execute runs, so the reaper never requeues a run that is
// still in progress.
func TestPool_LongCollectKeepsItsLease(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme-lease")
	ctx := context.Background()
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	reaped := -1
	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 3 * time.Second, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		time.Sleep(4500 * time.Millisecond) // longer than one lease
		n, err := global.ReapExpiredLeases(ctx, db)
		if err != nil {
			t.Errorf("reap: %v", err)
		}
		reaped = n
		return collector.Result{Facts: []collector.Fact{numFact("gitlab.commits_30d", 5)}}, nil
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaper took back %d run(s) mid-collect, want 0", reaped)
	}
}

// A run that fails for a reason retrying will not fix (config, internal)
// is not retried, but the binding comes due again after its interval —
// not on the enqueuer's next tick, which would rerun it every 30 seconds.
func TestPool_ConfigErrorWaitsOutTheInterval(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme-config-error")
	ctx := context.Background()
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{}, collector.ErrInternal(errors.New("trivy: not installed"))
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	due, err := repo.ListDueBindings(ctx, tenant.FromRun(db, orgID), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("the failed binding is due again right away: %+v", due)
	}
}

// runOnceAndNextSync runs bindingID's one queued run to success and
// returns how far out its next sync was put.
func runOnceAndNextSync(t *testing.T, db *sqlx.DB, orgID, connID, bindingID uuid.UUID) time.Duration {
	t.Helper()
	ctx := context.Background()
	if _, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "gitlab", Trigger: "manual", ScheduledFor: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pool := &scheduler.Pool{DB: db, WorkerID: "w1", LeaseDuration: 10 * time.Minute, Execute: func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		return collector.Result{Facts: []collector.Fact{numFact("gitlab.commits_30d", 5)}}, nil
	}}
	if _, err := pool.ClaimAndRun(ctx); err != nil {
		t.Fatalf("ClaimAndRun: %v", err)
	}
	binding, err := repo.BindingByID(ctx, tenant.FromRun(db, orgID), bindingID)
	if err != nil {
		t.Fatalf("BindingByID: %v", err)
	}
	return time.Until(*binding.NextSyncAt)
}

// floorCollector stands in for a kind's collector in the floor test: only
// its Descriptor is read. Registered for sonarqube, which no other test in
// this package registers, so no persister comes with it.
type floorCollector struct{}

func (floorCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{Kind: collector.KindSonarQube, MinInterval: 5 * time.Minute}
}

func (floorCollector) Open(context.Context, collector.ConnConfig) (collector.Session, error) {
	return nil, errors.New("not used")
}

func init() { collector.Register(collector.KindSonarQube, floorCollector{}) }

// With nothing set, a binding syncs hourly; an organization's default
// sync interval replaces that, but never below the collector's floor
// (MinInterval, 5 minutes here).
func TestPool_IntervalFallsBackToTheOrgDefaultThenAnHour(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	near := func(got, want time.Duration) bool { return got > want*85/100 && got < want*115/100 }

	orgID, connID, bindingID := seedFixture(t, db, "acme-default-interval")
	if d := runOnceAndNextSync(t, db, orgID, connID, bindingID); !near(d, time.Hour) {
		t.Errorf("unset: next sync in %v, want about an hour", d)
	}

	orgID, connID, bindingID = seedFixture(t, db, "acme-org-interval")
	if _, err := db.Exec(`UPDATE organizations SET default_sync_interval_seconds = 1800 WHERE id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	if d := runOnceAndNextSync(t, db, orgID, connID, bindingID); !near(d, 30*time.Minute) {
		t.Errorf("org default 30m: next sync in %v, want about 30 minutes", d)
	}

	orgID, connID, bindingID = seedFixture(t, db, "acme-org-floor")
	if _, err := db.Exec(`UPDATE organizations SET default_sync_interval_seconds = 60 WHERE id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET kind = 'sonarqube' WHERE id = $1`, bindingID); err != nil {
			t.Fatal(err)
		}
	})
	if d := runOnceAndNextSync(t, db, orgID, connID, bindingID); !near(d, 5*time.Minute) {
		t.Errorf("org default 1m: next sync in %v, want the collector's 5-minute floor", d)
	}
}
