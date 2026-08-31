package retention_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/retention"
)

// openDDL returns a connection with real DDL rights (CREATE/DROP TABLE),
// scoped to the same isolated per-test schema dbtest.Open would use —
// retention's own operations need the lotus_migrate role's privileges,
// which lotus_app (what dbtest.Open hands back) was deliberately never
// granted; see internal/db.OpenApp's own doc comment for why, and
// cmd/lotus/main.go's wiring of retention.Run for the production
// equivalent of this same distinction.
func openDDL(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := dbtest.DSN(t)
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open ddl conn: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedRetentionFixture(t *testing.T, db *sqlx.DB) (orgID, componentID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	var projectID uuid.UUID
	if err := db.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','P') RETURNING id`, orgID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := db.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','C','backend') RETURNING id`, orgID, projectID); err != nil {
		t.Fatalf("insert component: %v", err)
	}
	var connID uuid.UUID
	if err := db.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','g','https://x') RETURNING id`, orgID); err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	if err := db.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1,$2,$3,'gitlab','g/r') RETURNING id`,
		orgID, componentID, connID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	return orgID, componentID, bindingID
}

func TestEnsurePartitions_CreatesFutureMonths(t *testing.T) {
	db := openDDL(t)
	from := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC) // far future, guaranteed not pre-created by the migration

	if err := retention.EnsurePartitions(context.Background(), db, from, 3); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	for _, month := range []string{"2030_06", "2030_07", "2030_08"} {
		var exists bool
		if err := db.Get(&exists, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, "gitlab_snapshots_"+month); err != nil {
			t.Fatalf("check partition %s: %v", month, err)
		}
		if !exists {
			t.Errorf("partition gitlab_snapshots_%s was not created", month)
		}
	}

	// Idempotent: calling again must not error (IF NOT EXISTS).
	if err := retention.EnsurePartitions(context.Background(), db, from, 3); err != nil {
		t.Fatalf("EnsurePartitions (2nd call): %v", err)
	}
}

func TestDropStaleRawDetail_NullsRawJSONKeepsRow(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedRetentionFixture(t, db)

	old := time.Now().Add(-40 * 24 * time.Hour)
	// Migration 00018 builds partitions from the start of LAST month
	// onwards, so a 40-day-old row falls outside them during the first
	// days of any month — this test failed on 1 September with "no
	// partition of relation gitlab_snapshots found for row (23514)" and
	// would have failed for the first nine days of every month since it
	// was written. Create the partition the row needs instead of depending
	// on which day the suite happens to run.
	if err := retention.EnsurePartitions(context.Background(), db, old, 3); err != nil {
		t.Fatalf("EnsurePartitions covering the old row's month: %v", err)
	}
	var snapID uuid.UUID
	if err := db.Get(&snapID, `
		INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, captured_at, raw_json)
		VALUES ($1, $2, $3, $4, '{"a":1}'::jsonb) RETURNING id`,
		orgID, bindingID, componentID, old); err != nil {
		t.Fatalf("insert old snapshot: %v", err)
	}

	if err := retention.DropStaleRawDetail(context.Background(), db, time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("DropStaleRawDetail: %v", err)
	}

	var rawJSON sql.NullString
	if err := db.Get(&rawJSON, `SELECT raw_json::text FROM gitlab_snapshots WHERE id = $1`, snapID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if rawJSON.Valid {
		t.Errorf("raw_json = %q, want NULL after aging out", rawJSON.String)
	}
	var stillThere bool
	if err := db.Get(&stillThere, `SELECT EXISTS (SELECT 1 FROM gitlab_snapshots WHERE id = $1)`, snapID); err != nil {
		t.Fatalf("select exists: %v", err)
	}
	if !stillThere {
		t.Error("the row itself must survive — only raw_json is dropped, not the headline")
	}
}

func TestDropOldPartitions_DropsPastKeepsRecentAndSweepsSentryIssuesFirst(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedRetentionFixture(t, db)

	// A Sentry binding + an old snapshot + its issue, to prove the FK
	// against sentry_snapshots survives the whole operation (the issue
	// must be swept before its parent partition is dropped).
	var sentryConnID uuid.UUID
	if err := db.Get(&sentryConnID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'sentry','s','https://x') RETURNING id`, orgID); err != nil {
		t.Fatalf("insert sentry connection: %v", err)
	}
	var sentryBindingID uuid.UUID
	if err := db.Get(&sentryBindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1,$2,$3,'sentry','backend') RETURNING id`,
		orgID, componentID, sentryConnID); err != nil {
		t.Fatalf("insert sentry binding: %v", err)
	}

	farPast := time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)
	if err := retention.EnsurePartitions(context.Background(), db, farPast, 1); err != nil {
		t.Fatalf("EnsurePartitions (far past): %v", err)
	}
	var oldSentrySnapID uuid.UUID
	var oldSentryCapturedAt time.Time
	if err := db.QueryRow(`
		INSERT INTO sentry_snapshots (org_id, binding_id, component_id, captured_at, environment, project_slug)
		VALUES ($1,$2,$3,$4,'production','backend') RETURNING id, captured_at`,
		orgID, sentryBindingID, componentID, farPast).Scan(&oldSentrySnapID, &oldSentryCapturedAt); err != nil {
		t.Fatalf("insert old sentry snapshot: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sentry_issues (org_id, snapshot_id, snapshot_captured_at, issue_id) VALUES ($1,$2,$3,'ISSUE-1')`,
		orgID, oldSentrySnapID, oldSentryCapturedAt); err != nil {
		t.Fatalf("insert old sentry issue: %v", err)
	}

	// A recent gitlab snapshot, inside the retention window, must survive.
	recent := time.Now().Add(-5 * 24 * time.Hour)
	var recentSnapID uuid.UUID
	if err := db.Get(&recentSnapID, `
		INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, captured_at)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		orgID, bindingID, componentID, recent); err != nil {
		t.Fatalf("insert recent snapshot: %v", err)
	}

	if err := retention.DropOldPartitions(context.Background(), db, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("DropOldPartitions: %v", err)
	}

	var partitionGone bool
	if err := db.Get(&partitionGone, `SELECT NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'sentry_snapshots_2020_01')`); err != nil {
		t.Fatalf("check partition dropped: %v", err)
	}
	if !partitionGone {
		t.Error("sentry_snapshots_2020_01 should have been dropped")
	}

	var issueCount int
	if err := db.Get(&issueCount, `SELECT count(*) FROM sentry_issues WHERE snapshot_id = $1`, oldSentrySnapID); err != nil {
		t.Fatalf("count sentry_issues: %v", err)
	}
	if issueCount != 0 {
		t.Errorf("old sentry_issues rows = %d, want 0 (swept ahead of the partition drop)", issueCount)
	}

	var recentStillThere bool
	if err := db.Get(&recentStillThere, `SELECT EXISTS (SELECT 1 FROM gitlab_snapshots WHERE id = $1)`, recentSnapID); err != nil {
		t.Fatalf("check recent survives: %v", err)
	}
	if !recentStillThere {
		t.Error("a snapshot inside the retention window must not be dropped")
	}
}

func TestDeleteOldSyncRuns(t *testing.T) {
	db := openDDL(t)
	orgID, _, bindingID := seedRetentionFixture(t, db)
	var connID uuid.UUID
	if err := db.Get(&connID, `SELECT connection_id FROM metric_bindings WHERE id = $1`, bindingID); err != nil {
		t.Fatalf("lookup connection: %v", err)
	}

	// status='ok' on both: uq_run_inflight (migration 00013) is a partial
	// unique index on binding_id only for queued/leased/running rows, so
	// two finished runs on the same binding need a terminal status to
	// coexist at all.
	old := time.Now().Add(-40 * 24 * time.Hour)
	var oldRunID, recentRunID int64
	if err := db.Get(&oldRunID, `
		INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status, created_at)
		VALUES ($1,$2,$3,'gitlab','manual','ok',$4) RETURNING id`, orgID, connID, bindingID, old); err != nil {
		t.Fatalf("insert old run: %v", err)
	}
	if err := db.Get(&recentRunID, `
		INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, status)
		VALUES ($1,$2,$3,'gitlab','manual','ok') RETURNING id`, orgID, connID, bindingID); err != nil {
		t.Fatalf("insert recent run: %v", err)
	}

	if err := retention.DeleteOldSyncRuns(context.Background(), db, time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("DeleteOldSyncRuns: %v", err)
	}

	var oldExists, recentExists bool
	db.Get(&oldExists, `SELECT EXISTS (SELECT 1 FROM sync_runs WHERE id = $1)`, oldRunID)       //nolint:errcheck
	db.Get(&recentExists, `SELECT EXISTS (SELECT 1 FROM sync_runs WHERE id = $1)`, recentRunID) //nolint:errcheck
	if oldExists {
		t.Error("a sync_runs row older than the window must be deleted")
	}
	if !recentExists {
		t.Error("a sync_runs row inside the window must survive")
	}
}

// TestDropOldPartitions_IsFastRegardlessOfRowCount is task 3.6's own
// acceptance criterion — quoted directly, the whole reason these tables
// are partitioned at all: "selisih antara maintenance window 3 detik dan
// insiden vacuum" (the difference between a 3-second maintenance window
// and a vacuum incident). DROP TABLE on a partition is a catalog-only
// operation: its cost is independent of how many rows the partition
// holds, unlike a bulk DELETE (which would scan and log every row, then
// leave a vacuum-sized mess behind). Proven here by dropping a partition
// holding several thousand rows and asserting it completes in well under
// a second — a row-by-row DELETE at this volume would not.
//
// (A DROP does still take a brief lock on the parent table to update its
// partition map — like any DDL touching a table's structure, it isn't
// magically free of locking altogether. What task 3.6 is actually
// contrasting it with is a bulk DELETE's cost: a scan proportional to
// row count, WAL volume proportional to row count, and the vacuum/bloat
// cleanup that follows. This test's threshold is chosen to demonstrate
// that difference, not to claim zero interaction with concurrent
// transactions under every possible interleaving.)
func TestDropOldPartitions_IsFastRegardlessOfRowCount(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedRetentionFixture(t, db)

	farPast := time.Date(2019, 3, 10, 0, 0, 0, 0, time.UTC)
	if err := retention.EnsurePartitions(context.Background(), db, farPast, 1); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, captured_at)
		SELECT $1, $2, $3, $4 FROM generate_series(1, 5000)`,
		orgID, bindingID, componentID, farPast); err != nil {
		t.Fatalf("seed 5000 old rows: %v", err)
	}

	start := time.Now()
	if err := retention.DropOldPartitions(context.Background(), db, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("DropOldPartitions: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("DropOldPartitions of a 5000-row partition took %v, want well under 1s (catalog-only drop, not a row-by-row delete)", elapsed)
	}

	var exists bool
	if err := db.Get(&exists, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'gitlab_snapshots_2019_03')`); err != nil {
		t.Fatalf("check partition dropped: %v", err)
	}
	if exists {
		t.Error("gitlab_snapshots_2019_03 should have been dropped")
	}
}
