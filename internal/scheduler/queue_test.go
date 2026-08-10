package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
)

func TestEnqueueImmediate_NewBindingGetsARunRightAway(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme")

	if err := scheduler.EnqueueImmediate(context.Background(), db, orgID, connID, bindingID, "gitlab"); err != nil {
		t.Fatalf("EnqueueImmediate: %v", err)
	}

	run, err := global.ClaimRun(context.Background(), db, "w1", nil, 10*time.Minute)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run to be immediately claimable after EnqueueImmediate")
	}
	if run.Trigger != "manual" {
		t.Fatalf("trigger = %q, want manual", run.Trigger)
	}
}

func TestEnqueueDueBindingsForOrg_OnlyEnqueuesDueBindings(t *testing.T) {
	db := dbtest.Open(t)
	orgID, _, bindingID := seedFixture(t, db, "acme")
	ctx := context.Background()

	// Not due yet. metric_bindings is RLS'd (task 0.3) — a plain db.Exec
	// with no app.org_id set would silently update zero rows, not error,
	// so this must go through the org's own scope like any other write.
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET next_sync_at = now() + interval '1 hour' WHERE id = $1`, bindingID); err != nil {
			t.Fatalf("set next_sync_at: %v", err)
		}
	})
	n, err := scheduler.EnqueueDueBindingsForOrg(ctx, db, orgID)
	if err != nil {
		t.Fatalf("EnqueueDueBindingsForOrg: %v", err)
	}
	if n != 0 {
		t.Fatalf("enqueued %d, want 0 (binding isn't due yet)", n)
	}

	// Now due.
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET next_sync_at = now() - interval '1 minute' WHERE id = $1`, bindingID); err != nil {
			t.Fatalf("set next_sync_at: %v", err)
		}
	})
	n, err = scheduler.EnqueueDueBindingsForOrg(ctx, db, orgID)
	if err != nil {
		t.Fatalf("EnqueueDueBindingsForOrg: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d, want 1", n)
	}
}

func TestEnqueueDueBindingsForOrg_DisabledBindingNeverEnqueued(t *testing.T) {
	db := dbtest.Open(t)
	orgID, _, bindingID := seedFixture(t, db, "acme")
	ctx := context.Background()

	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET enabled = false WHERE id = $1`, bindingID); err != nil {
			t.Fatalf("disable binding: %v", err)
		}
	})
	n, err := scheduler.EnqueueDueBindingsForOrg(ctx, db, orgID)
	if err != nil {
		t.Fatalf("EnqueueDueBindingsForOrg: %v", err)
	}
	if n != 0 {
		t.Fatalf("enqueued %d, want 0 (disabled binding must never be scheduled)", n)
	}
}

func TestEnqueueDueBindingsAllOrgs_CoversEveryOrg(t *testing.T) {
	db := dbtest.Open(t)
	seedFixture(t, db, "acme")
	seedFixture(t, db, "beta")
	ctx := context.Background()

	n, err := scheduler.EnqueueDueBindingsAllOrgs(ctx, db)
	if err != nil {
		t.Fatalf("EnqueueDueBindingsAllOrgs: %v", err)
	}
	if n != 2 {
		t.Fatalf("enqueued %d, want 2 (both orgs' brand-new bindings are immediately due)", n)
	}
}
