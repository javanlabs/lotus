package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	lotusdb "github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/retention"
	"github.com/uyab/lotus/internal/tenant"
	"github.com/uyab/lotus/internal/timeseries"
)

func sonarBindingFixture(t *testing.T, sub string) (tenant.Scope, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := dbtest.DSN(t)
	// Past months need their partitions first — DDL the app role lacks,
	// as `lotus import-legacy` does with the migrate role.
	ddl, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ddl.Close() })
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := retention.EnsurePartitions(context.Background(), ddl, from, 12); err != nil {
		t.Fatal(err)
	}
	if err := timeseries.EnsurePartitions(context.Background(), ddl, from, 12); err != nil {
		t.Fatal(err)
	}
	db, err := lotusdb.OpenApp(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, s := folderTestOrg(t, db, sub)
	p := mustProject(t, s, "portal", nil)
	var componentID, bindingID uuid.UUID
	if err := s.Tx(context.Background(), func(tx *sqlx.Tx) error {
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'api', 'api', 'backend') RETURNING id`, s.OrgID(), p.ID); err != nil {
			return err
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'sonarqube', 'Sonar', 'https://sonar.example') RETURNING id`, s.OrgID()); err != nil {
			return err
		}
		return tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, 'sonarqube', 'api') RETURNING id`, s.OrgID(), componentID, connID)
	}); err != nil {
		t.Fatal(err)
	}
	return s, componentID, bindingID
}

// An import writes history at its own time (WithObservedAt): snapshots
// captured and facts collected then, deduplicated against the history up
// to then only — never touching a live row — and leaving the dashboard's
// current state to live collection.
func TestWithObservedAt_WritesHistoryWithoutTouchingTheLiveState(t *testing.T) {
	s, componentID, bindingID := sonarBindingFixture(t, "acme-observed")
	ctx := context.Background()
	cov := func(v float64) *float64 { return &v }
	sonar := func(c float64) repo.NewSonarSnapshot {
		return repo.NewSonarSnapshot{BindingID: bindingID, ComponentID: componentID, Coverage: cov(c)}
	}

	// Live collection first: coverage 80, current state "live".
	if _, _, err := repo.InsertSonarSnapshot(ctx, s, sonar(80), "hash-80", collector.Headline{Score: intp(80), Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	// Then an import of the months before, oldest first — one of them with
	// the very same content as the live row.
	may := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC)
	june := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	for _, obs := range []struct {
		at   time.Time
		c    float64
		hash string
	}{{may, 70, "hash-70"}, {may.Add(time.Hour), 70, "hash-70"}, {june, 80, "hash-80"}} {
		octx := repo.WithObservedAt(ctx, obs.at)
		if _, _, err := repo.InsertSonarSnapshot(octx, s, sonar(obs.c), obs.hash, collector.Headline{Score: intp(1), Status: "fail"}); err != nil {
			t.Fatal(err)
		}
		if err := repo.InsertMetricFacts(octx, s, componentID, bindingID, "sonarqube", collector.Result{
			Facts: []collector.Fact{{Key: "sonar.coverage", ValueNum: cov(obs.c), Unit: "%"}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var snaps []struct {
		Captured  time.Time `db:"captured_at"`
		Collected time.Time `db:"collected_at"`
		Hash      string    `db:"content_hash"`
	}
	var facts []time.Time
	var score int
	if err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.Select(&snaps, `SELECT captured_at, collected_at, content_hash FROM sonar_snapshots WHERE binding_id = $1 ORDER BY captured_at`, bindingID); err != nil {
			return err
		}
		if err := tx.Select(&facts, `SELECT collected_at FROM metric_facts WHERE binding_id = $1 ORDER BY collected_at`, bindingID); err != nil {
			return err
		}
		return tx.Get(&score, `SELECT score FROM component_metrics_current WHERE component_id = $1`, componentID)
	}); err != nil {
		t.Fatal(err)
	}
	// May (touched once, an hour later), June (its own row: the live row is
	// later, so it is not June's predecessor), and the live row, untouched.
	if len(snaps) != 3 || !snaps[0].Captured.Equal(may) || !snaps[0].Collected.Equal(may.Add(time.Hour)) ||
		!snaps[1].Captured.Equal(june) || snaps[2].Captured.Before(june.AddDate(0, 1, 0)) || snaps[2].Collected.Before(snaps[2].Captured) {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if len(facts) != 3 || !facts[0].Equal(may) || !facts[2].Equal(june) {
		t.Errorf("facts collected at %v; want May, May+1h, June", facts)
	}
	if score != 80 {
		t.Errorf("current score = %d; the import must leave the live state alone", score)
	}
}

func intp(v int) *int { return &v }
