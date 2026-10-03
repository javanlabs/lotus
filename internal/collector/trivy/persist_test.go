package trivy_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxTrivy(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
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

func seedTrivyFixture(t *testing.T, db *sqlx.DB, subdomain string) (orgID, componentID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxTrivy(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'harbor', 'https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'trivy', 'proj/app') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, componentID, bindingID
}

// addSecondTrivyBinding replaces the component's Trivy binding with one
// watching another image: a component has one live binding per kind
// (migration 00035), so the first is retired, as ReplaceBinding does.
func addSecondTrivyBinding(t *testing.T, db *sqlx.DB, orgID, componentID uuid.UUID) (bindingID uuid.UUID) {
	t.Helper()
	withOrgTxTrivy(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`UPDATE metric_bindings SET enabled = false, deleted_at = now()
			WHERE component_id = $1 AND kind = 'trivy' AND deleted_at IS NULL`, componentID); err != nil {
			t.Fatalf("retire first binding: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'harbor2', 'https://harbor.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'trivy', 'proj/app-sidecar') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert second binding: %v", err)
		}
	})
	return bindingID
}

func TestPersist_PullResultSavesSnapshot(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedTrivyFixture(t, db, "acme")

	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	scope := tenant.FromRun(db, orgID)
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingID, result); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	snap, err := repo.LatestTrivySnapshot(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("LatestTrivySnapshot: %v", err)
	}
	if snap.Source != "registry" {
		t.Fatalf("Source = %q, want registry", snap.Source)
	}
	if snap.Critical != 1 || snap.High != 2 {
		t.Fatalf("Critical/High = %d/%d, want 1/2", snap.Critical, snap.High)
	}
	if len(snap.Tags) != 2 {
		t.Fatalf("Tags = %v, want 2 tags", snap.Tags)
	}
}

func TestPersist_PushAndPullProduceSameRowShapeDifferingOnlyBySource(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedTrivyFixture(t, db, "acme")
	scope := tenant.FromRun(db, orgID)

	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	pullResult, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	session.Close()
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingID, pullResult); err != nil {
		t.Fatalf("persist pull: %v", err)
	}

	ing, _ := collector.GetIngestor(collector.KindTrivy)
	pushResult, err := ing.Parse(context.Background(), []byte(fsReportFixture), collector.IngestMeta{
		OrgID: orgID, ComponentID: componentID, BindingID: bindingID,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingID, pushResult); err != nil {
		t.Fatalf("persist push: %v", err)
	}

	snap, err := repo.LatestTrivySnapshot(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("LatestTrivySnapshot: %v", err)
	}
	if snap.Source != "push" {
		t.Fatalf("Source = %q, want push (most recently persisted)", snap.Source)
	}
	if snap.Critical != 1 || snap.High != 1 {
		t.Fatalf("Critical/High = %d/%d, want 1/1 (from the push fixture)", snap.Critical, snap.High)
	}
}

func TestListTrivySnapshotsForComponent_ReplacedBindingKeepsItsOwnSnapshot(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingA := seedTrivyFixture(t, db, "acme")
	bindingB := addSecondTrivyBinding(t, db, orgID, componentID)
	scope := tenant.FromRun(db, orgID)

	srv := fakeHarborServer(t)
	defer srv.Close()
	sessionA := openTrivySession(t, srv, "good-user:good-pass")
	resultA, err := sessionA.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("collect A: %v", err)
	}
	sessionA.Close()
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingA, resultA); err != nil {
		t.Fatalf("persist A: %v", err)
	}

	ing, _ := collector.GetIngestor(collector.KindTrivy)
	resultB, err := ing.Parse(context.Background(), []byte(fsReportFixture), collector.IngestMeta{
		OrgID: orgID, ComponentID: componentID, BindingID: bindingB,
	})
	if err != nil {
		t.Fatalf("parse B: %v", err)
	}
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingB, resultB); err != nil {
		t.Fatalf("persist B: %v", err)
	}

	snaps, err := repo.ListTrivySnapshotsForComponent(context.Background(), scope, componentID)
	if err != nil {
		t.Fatalf("ListTrivySnapshotsForComponent: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (the replaced binding's and the new one's)", len(snaps))
	}
	seen := map[uuid.UUID]bool{}
	for _, s := range snaps {
		seen[s.BindingID] = true
	}
	if !seen[bindingA] || !seen[bindingB] {
		t.Fatalf("expected both bindings represented, got %+v", seen)
	}
}

func TestPersist_CrossOrgSnapshotsNeverMixDespiteIdenticalExternalKey(t *testing.T) {
	db := dbtest.Open(t)
	orgA, compA, bindA := seedTrivyFixture(t, db, "acme")
	orgB, compB, bindB := seedTrivyFixture(t, db, "beta") // same external_key "proj/app" in both orgs

	srv := fakeHarborServer(t)
	defer srv.Close()

	sessionA := openTrivySession(t, srv, "good-user:good-pass")
	resultA, err := sessionA.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("collect A: %v", err)
	}
	sessionA.Close()
	sessionB := openTrivySession(t, srv, "good-user:good-pass")
	resultB, err := sessionB.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("collect B: %v", err)
	}
	sessionB.Close()

	scopeA := tenant.FromRun(db, orgA)
	scopeB := tenant.FromRun(db, orgB)
	if err := trivycollector.Persist(context.Background(), scopeA, compA, bindA, resultA); err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if err := trivycollector.Persist(context.Background(), scopeB, compB, bindB, resultB); err != nil {
		t.Fatalf("persist B: %v", err)
	}

	snapA, err := repo.LatestTrivySnapshot(context.Background(), scopeA, bindA)
	if err != nil {
		t.Fatalf("LatestTrivySnapshot A: %v", err)
	}
	snapB, err := repo.LatestTrivySnapshot(context.Background(), scopeB, bindB)
	if err != nil {
		t.Fatalf("LatestTrivySnapshot B: %v", err)
	}
	if snapA.ID == snapB.ID {
		t.Fatal("org A and org B must never share a snapshot row, even with an identical external_key")
	}

	// org A cannot see org B's snapshot even by binding id (RLS).
	_, err = repo.LatestTrivySnapshot(context.Background(), scopeA, bindB)
	if err == nil {
		t.Fatal("expected org A's scope to not see org B's snapshot")
	}
}
