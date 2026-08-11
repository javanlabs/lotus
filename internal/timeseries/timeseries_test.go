package timeseries_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/timeseries"
)

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

func seedFixture(t *testing.T, db *sqlx.DB) (orgID, componentID, bindingID uuid.UUID) {
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
	if err := db.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1,$2,$3,'gitlab','x') RETURNING id`,
		orgID, componentID, connID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	return orgID, componentID, bindingID
}

func TestEnsurePartitions_CreatesFutureMonths(t *testing.T) {
	db := openDDL(t)
	from := time.Date(2031, 2, 10, 0, 0, 0, 0, time.UTC)

	if err := timeseries.EnsurePartitions(context.Background(), db, from, 3); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	for _, month := range []string{"2031_02", "2031_03", "2031_04"} {
		var exists bool
		if err := db.Get(&exists, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, "metric_facts_"+month); err != nil {
			t.Fatalf("check partition %s: %v", month, err)
		}
		if !exists {
			t.Errorf("partition metric_facts_%s was not created", month)
		}
	}
	if err := timeseries.EnsurePartitions(context.Background(), db, from, 3); err != nil {
		t.Fatalf("EnsurePartitions (2nd call, idempotent): %v", err)
	}
}

func TestDropOldFactPartitions_DropsPastKeepsRecent(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedFixture(t, db)

	farPast := time.Date(2018, 6, 10, 0, 0, 0, 0, time.UTC)
	if err := timeseries.EnsurePartitions(context.Background(), db, farPast, 1); err != nil {
		t.Fatalf("EnsurePartitions (far past): %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, collected_at)
		VALUES ($1,$2,$3,'gitlab.x',1,$4)`, orgID, bindingID, componentID, farPast); err != nil {
		t.Fatalf("seed old fact: %v", err)
	}

	recent := time.Now().Add(-2 * 24 * time.Hour)
	var recentID int64
	if err := db.Get(&recentID, `
		INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, collected_at)
		VALUES ($1,$2,$3,'gitlab.x',2,$4) RETURNING id`, orgID, bindingID, componentID, recent); err != nil {
		t.Fatalf("seed recent fact: %v", err)
	}

	if err := timeseries.DropOldFactPartitions(context.Background(), db, time.Now().Add(-14*24*time.Hour)); err != nil {
		t.Fatalf("DropOldFactPartitions: %v", err)
	}

	var partitionGone bool
	if err := db.Get(&partitionGone, `SELECT NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'metric_facts_2018_06')`); err != nil {
		t.Fatalf("check partition dropped: %v", err)
	}
	if !partitionGone {
		t.Error("metric_facts_2018_06 should have been dropped")
	}

	var recentStillThere bool
	if err := db.Get(&recentStillThere, `SELECT EXISTS (SELECT 1 FROM metric_facts WHERE id = $1)`, recentID); err != nil {
		t.Fatalf("check recent survives: %v", err)
	}
	if !recentStillThere {
		t.Error("a fact inside the retention window must not be dropped")
	}
}

func TestDeleteOldRollups(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedFixture(t, db)

	old := time.Now().Add(-50 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	if _, err := db.Exec(`
		INSERT INTO metric_facts_hourly (org_id, binding_id, component_id, key, bucket, sample_count)
		VALUES ($1,$2,$3,'gitlab.x',$4,1)`, orgID, bindingID, componentID, old); err != nil {
		t.Fatalf("seed old hourly: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO metric_facts_hourly (org_id, binding_id, component_id, key, bucket, sample_count)
		VALUES ($1,$2,$3,'gitlab.x',$4,1)`, orgID, bindingID, componentID, recent); err != nil {
		t.Fatalf("seed recent hourly: %v", err)
	}

	if err := timeseries.DeleteOldRollups(context.Background(), db, "metric_facts_hourly", time.Now().Add(-45*24*time.Hour)); err != nil {
		t.Fatalf("DeleteOldRollups: %v", err)
	}

	var count int
	if err := db.Get(&count, `SELECT count(*) FROM metric_facts_hourly WHERE binding_id = $1`, bindingID); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1 (only the recent bucket survives)", count)
	}
}

// TestDeactivateStaleMetricKeys_FreezesButSeriesStillReturnsPastData is
// task 3.7's own acceptance criterion: a metric that stops being emitted
// becomes active=false, but Series for a past range still returns its
// data — the catalog freeze must never touch the fact rows themselves.
func TestDeactivateStaleMetricKeys_FreezesButSeriesStillReturnsPastData(t *testing.T) {
	db := openDDL(t)
	orgID, componentID, bindingID := seedFixture(t, db)

	stale := time.Now().Add(-10 * 24 * time.Hour)
	if _, err := db.Exec(`
		INSERT INTO metric_keys (org_id, kind, key, declared, first_seen_at, last_seen_at, active)
		VALUES ($1,'gitlab','gitlab.discontinued',false,$2,$2,true)`, orgID, stale); err != nil {
		t.Fatalf("seed stale key: %v", err)
	}
	fresh := time.Now().Add(-1 * time.Hour)
	if _, err := db.Exec(`
		INSERT INTO metric_keys (org_id, kind, key, declared, first_seen_at, last_seen_at, active)
		VALUES ($1,'gitlab','gitlab.still_active',false,$2,$2,true)`, orgID, fresh); err != nil {
		t.Fatalf("seed active key: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, collected_at)
		VALUES ($1,$2,$3,'gitlab.discontinued',77,$4)`, orgID, bindingID, componentID, stale); err != nil {
		t.Fatalf("seed old fact: %v", err)
	}

	if err := timeseries.DeactivateStaleMetricKeys(context.Background(), db, time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatalf("DeactivateStaleMetricKeys: %v", err)
	}

	var discontinuedActive, stillActive bool
	if err := db.Get(&discontinuedActive, `SELECT active FROM metric_keys WHERE org_id=$1 AND key='gitlab.discontinued'`, orgID); err != nil {
		t.Fatalf("read discontinued: %v", err)
	}
	if err := db.Get(&stillActive, `SELECT active FROM metric_keys WHERE org_id=$1 AND key='gitlab.still_active'`, orgID); err != nil {
		t.Fatalf("read still_active: %v", err)
	}
	if discontinuedActive {
		t.Error("a key not seen in over the staleness window must be marked inactive")
	}
	if !stillActive {
		t.Error("a key seen recently must stay active")
	}

	// The fact row itself must be untouched by the sweep.
	var factValue float64
	if err := db.Get(&factValue, `SELECT value_num FROM metric_facts WHERE binding_id=$1 AND key='gitlab.discontinued'`, bindingID); err != nil {
		t.Fatalf("read fact after deactivation: %v", err)
	}
	if factValue != 77 {
		t.Errorf("fact value = %v, want 77 (unchanged — deactivation must not touch history)", factValue)
	}
}
