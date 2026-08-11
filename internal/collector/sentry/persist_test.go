package sentry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	sentrycollector "github.com/uyab/lotus/internal/collector/sentry"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxSentry(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
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

func seedSentryFixture(t *testing.T, db *sqlx.DB, subdomain string) (orgID, componentID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxSentry(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'sentry', 'sentry', 'https://sentry.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'sentry', 'backend') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, componentID, bindingID
}

func TestPersist_TwoEnvironmentsBothSaved(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme")

	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production", "staging"}})
	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	scope := tenant.FromRun(db, orgID)
	if err := sentrycollector.Persist(context.Background(), scope, componentID, bindingID, result); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	snaps, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("ListSentrySnapshotsForBinding: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (production + staging)", len(snaps))
	}

	issues, err := repo.ListSentryIssuesForSnapshot(context.Background(), scope, snaps[0].ID)
	if err != nil {
		t.Fatalf("ListSentryIssuesForSnapshot: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues for the snapshot, want 2", len(issues))
	}
}

// TestPersist_IdenticalSecondCollectTouchesInsteadOfReinserting is task
// 3.6's own acceptance criterion, exercised against sentry's per-
// environment dedupe path specifically: identical upstream issue data on
// a second collect must not create a new snapshot row (or duplicate its
// issues), and must not violate sentry_issues' FK to the now-unchanged
// parent row.
func TestPersist_IdenticalSecondCollectTouchesInsteadOfReinserting(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated"})

	collectAndPersist := func() {
		session := openSession(t, srv, "javan")
		defer session.Close()
		result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if err := sentrycollector.Persist(context.Background(), scope, componentID, bindingID, result); err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	collectAndPersist()
	first, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("list 1: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("got %d snapshots after first collect, want 1", len(first))
	}

	collectAndPersist()
	second, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("got %d snapshots after second identical collect, want 1 (touched, not reinserted)", len(second))
	}
	if second[0].ID != first[0].ID {
		t.Fatalf("a second identical collect must touch the existing row, not replace it (first=%s, second=%s)", first[0].ID, second[0].ID)
	}
	if !second[0].CollectedAt.After(first[0].CollectedAt) {
		t.Fatalf("collected_at must advance on a touch (first=%v, second=%v)", first[0].CollectedAt, second[0].CollectedAt)
	}

	issues, err := repo.ListSentryIssuesForSnapshot(context.Background(), scope, second[0].ID)
	if err != nil {
		t.Fatalf("ListSentryIssuesForSnapshot: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues after touch, want the original 2 (still intact, FK unbroken)", len(issues))
	}
}

func TestPersist_ReplaceDropsEnvironmentMissingFromNewCollect(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedSentryFixture(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	srv := fakeSentryServer(t, nil)
	defer srv.Close()

	// First Collect: two environments.
	session1 := openSession(t, srv, "javan")
	cfg2env, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production", "staging"}})
	result1, err := session1.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg2env})
	if err != nil {
		t.Fatalf("Collect 1: %v", err)
	}
	session1.Close()
	if err := sentrycollector.Persist(context.Background(), scope, componentID, bindingID, result1); err != nil {
		t.Fatalf("Persist 1: %v", err)
	}

	// Second Collect: binding reconfigured down to just "production" —
	// the REPLACE must drop "staging" for this binding, per task 3.5's
	// "pruning becomes trivial because it's scoped to binding_id".
	session2 := openSession(t, srv, "javan")
	cfg1env, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production"}})
	result2, err := session2.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg1env})
	if err != nil {
		t.Fatalf("Collect 2: %v", err)
	}
	session2.Close()
	if err := sentrycollector.Persist(context.Background(), scope, componentID, bindingID, result2); err != nil {
		t.Fatalf("Persist 2: %v", err)
	}

	snaps, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("ListSentrySnapshotsForBinding: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Environment != "production" {
		t.Fatalf("snapshots = %+v, want exactly one, environment=production", snaps)
	}
}

func TestPersist_EnvironmentPruneScopedToOneBindingOnly(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingA := seedSentryFixture(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	// A second binding on the same component/connection, also with two
	// environments — pruning binding A must never touch binding B.
	var bindingB uuid.UUID
	withOrgTxSentry(t, db, orgID, func(tx *sqlx.Tx) {
		var connID uuid.UUID
		if err := tx.Get(&connID, `SELECT id FROM integration_connections WHERE org_id = $1`, orgID); err != nil {
			t.Fatalf("lookup connection: %v", err)
		}
		if err := tx.Get(&bindingB, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'sentry', 'frontend') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding B: %v", err)
		}
	})

	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	cfg2env, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production", "staging"}})

	for _, b := range []struct {
		bindingID   uuid.UUID
		externalKey string
	}{{bindingA, "backend"}, {bindingB, "frontend"}} {
		session := openSession(t, srv, "javan")
		result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: b.externalKey, Config: cfg2env})
		if err != nil {
			t.Fatalf("Collect %s: %v", b.externalKey, err)
		}
		session.Close()
		if err := sentrycollector.Persist(context.Background(), scope, componentID, b.bindingID, result); err != nil {
			t.Fatalf("Persist %s: %v", b.externalKey, err)
		}
	}

	// Re-collect binding A down to one environment; binding B must keep
	// both of its own.
	session := openSession(t, srv, "javan")
	cfg1env, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production"}})
	resultA, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg1env})
	if err != nil {
		t.Fatalf("Collect A again: %v", err)
	}
	session.Close()
	if err := sentrycollector.Persist(context.Background(), scope, componentID, bindingA, resultA); err != nil {
		t.Fatalf("Persist A again: %v", err)
	}

	snapsA, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingA)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(snapsA) != 1 {
		t.Fatalf("binding A snapshots = %d, want 1", len(snapsA))
	}
	snapsB, err := repo.ListSentrySnapshotsForBinding(context.Background(), scope, bindingB)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(snapsB) != 2 {
		t.Fatalf("binding B snapshots = %d, want untouched at 2", len(snapsB))
	}
}

func TestPersist_CrossOrgSnapshotsNeverMixDespiteIdenticalExternalKey(t *testing.T) {
	db := dbtest.Open(t)
	orgA, compA, bindA := seedSentryFixture(t, db, "acme")
	orgB, compB, bindB := seedSentryFixture(t, db, "beta") // same external_key "backend" in both orgs

	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated"})

	sessionA := openSession(t, srv, "javan")
	resultA, err := sessionA.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("collect A: %v", err)
	}
	sessionA.Close()
	sessionB := openSession(t, srv, "javan")
	resultB, err := sessionB.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("collect B: %v", err)
	}
	sessionB.Close()

	scopeA := tenant.FromRun(db, orgA)
	scopeB := tenant.FromRun(db, orgB)
	if err := sentrycollector.Persist(context.Background(), scopeA, compA, bindA, resultA); err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if err := sentrycollector.Persist(context.Background(), scopeB, compB, bindB, resultB); err != nil {
		t.Fatalf("persist B: %v", err)
	}

	snapsA, err := repo.ListSentrySnapshotsForBinding(context.Background(), scopeA, bindA)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	snapsB, err := repo.ListSentrySnapshotsForBinding(context.Background(), scopeB, bindB)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(snapsA) != 1 || len(snapsB) != 1 || snapsA[0].ID == snapsB[0].ID {
		t.Fatal("org A and org B must never share a snapshot row, even with an identical external_key")
	}

	// org A cannot see org B's snapshot even by binding id (RLS).
	empty, err := repo.ListSentrySnapshotsForBinding(context.Background(), scopeA, bindB)
	if err != nil {
		t.Fatalf("cross-org list: %v", err)
	}
	if len(empty) != 0 {
		t.Fatal("expected org A's scope to not see org B's snapshot")
	}
}
