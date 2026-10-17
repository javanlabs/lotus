package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
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

// A component with a GitLab binding and no container image gets a
// repository-scan Trivy binding on the same connection and project path,
// due daily; running it again adds nothing, and deleting the scan turns
// it off for good.
func TestEnsureRepoScanBindings(t *testing.T) {
	db := dbtest.Open(t)
	orgID, connID, gitlabID := seedFixture(t, db, "acme-reposcan")
	ctx := context.Background()
	scope := tenant.FromRun(db, orgID)

	n, err := scheduler.EnsureRepoScanBindingsAllOrgs(ctx, db)
	if err != nil || n != 1 {
		t.Fatalf("first pass = %d, %v; want 1 created", n, err)
	}
	gl, err := repo.BindingByID(ctx, scope, gitlabID)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := repo.ActiveBindingForKind(ctx, scope, gl.ComponentID, "trivy")
	if err != nil {
		t.Fatalf("no trivy binding: %v", err)
	}
	if !repo.IsRepoScan(scan) || scan.ConnectionID != connID || scan.ExternalKey != "group/repo" ||
		scan.SyncIntervalSeconds == nil || *scan.SyncIntervalSeconds != 86400 {
		t.Fatalf("repository scan binding = %+v", scan)
	}

	if n, err := scheduler.EnsureRepoScanBindingsAllOrgs(ctx, db); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v; want nothing new", n, err)
	}

	if err := repo.SoftDeleteBinding(ctx, scope, scan.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, err := scheduler.EnsureRepoScanBindingsAllOrgs(ctx, db); err != nil || n != 0 {
		t.Fatalf("after deleting the scan = %d, %v; want it to stay off", n, err)
	}
}

// A component that has a container image (a Harbor Trivy binding) is
// scanned by its image, not its repository.
func TestEnsureRepoScanBindings_ImageWins(t *testing.T) {
	db := dbtest.Open(t)
	orgID, _, gitlabID := seedFixture(t, db, "acme-reposcan-image")
	ctx := context.Background()
	withOrgTxSchedTest(t, db, orgID, func(tx *sqlx.Tx) {
		var harbor uuid.UUID
		if err := tx.Get(&harbor, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'h', 'https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			SELECT org_id, component_id, $2, 'trivy', 'proj/app' FROM metric_bindings WHERE id = $1`, gitlabID, harbor); err != nil {
			t.Fatal(err)
		}
	})
	if n, err := scheduler.EnsureRepoScanBindingsAllOrgs(ctx, db); err != nil || n != 0 {
		t.Fatalf("= %d, %v; want no repository scan beside an image", n, err)
	}
}
