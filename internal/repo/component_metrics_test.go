package repo_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxCM(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
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

func seedProjectComponent(t *testing.T, db *sqlx.DB, orgID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	var componentID uuid.UUID
	withOrgTxCM(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, orgID, slug); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
	})
	return componentID
}

func TestUpsertComponentMetric_RollsUpWorstStatusAndAverageScore(t *testing.T) {
	db := dbtest.Open(t)
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	componentID := seedProjectComponent(t, db, orgID, "p")
	scope := tenant.FromRun(db, orgID)

	passScore, warnScore := 90, 60
	if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, "gitlab",
		collector.Headline{Status: collector.StatusPass, Score: &passScore}); err != nil {
		t.Fatalf("upsert gitlab: %v", err)
	}
	if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, "sonarqube",
		collector.Headline{Status: collector.StatusWarn, Score: &warnScore}); err != nil {
		t.Fatalf("upsert sonarqube: %v", err)
	}

	cm, err := repo.GetComponentMetric(context.Background(), scope, componentID)
	if err != nil {
		t.Fatalf("GetComponentMetric: %v", err)
	}
	if cm.StatusValue() != collector.StatusWarn {
		t.Fatalf("rollup status = %q, want warn (worst of pass/warn)", cm.Status)
	}
	if cm.Score == nil || *cm.Score != (passScore+warnScore)/2 {
		t.Fatalf("rollup score = %v, want average of %d and %d", cm.Score, passScore, warnScore)
	}
	metrics := cm.Metrics()
	if len(metrics) != 2 || metrics["gitlab"].Status != "pass" || metrics["sonarqube"].Status != "warn" {
		t.Fatalf("metrics map = %+v, want both kinds present with their own status", metrics)
	}
}

// TestUpsertComponentMetric_UnknownDoesNotDragDownRollup is task 3.6's own
// acceptance criterion: a component with a mix of pass and unknown must
// not be counted as failing, and unknown must not enter the percentage
// denominator (RollupStatus never returns fail/warn just because one kind
// is unknown; RollupScore ignores kinds with no score at all).
func TestUpsertComponentMetric_UnknownDoesNotDragDownRollup(t *testing.T) {
	db := dbtest.Open(t)
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	componentID := seedProjectComponent(t, db, orgID, "p")
	scope := tenant.FromRun(db, orgID)

	passScore := 95
	if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, "gitlab",
		collector.Headline{Status: collector.StatusPass, Score: &passScore}); err != nil {
		t.Fatalf("upsert gitlab: %v", err)
	}
	if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, "sentry",
		collector.Headline{Status: collector.StatusUnknown}); err != nil {
		t.Fatalf("upsert sentry: %v", err)
	}

	cm, err := repo.GetComponentMetric(context.Background(), scope, componentID)
	if err != nil {
		t.Fatalf("GetComponentMetric: %v", err)
	}
	if cm.StatusValue() != collector.StatusPass {
		t.Fatalf("rollup status = %q, want pass (unknown must not override a real pass)", cm.Status)
	}
	if cm.Score == nil || *cm.Score != passScore {
		t.Fatalf("rollup score = %v, want %d (the unscored unknown kind must not enter the average)", cm.Score, passScore)
	}
}

func TestUpsertComponentMetric_FifthFictionalKindNeedsNoQueryChange(t *testing.T) {
	// task 3.6's own acceptance criterion, stated literally: adding a
	// fictional fifth kind must require no change to the dashboard query.
	// Since metrics is a plain jsonb map keyed by kind name, this is true
	// by construction — proven here by writing an entirely made-up kind
	// and reading it back through the exact same generic read path.
	db := dbtest.Open(t)
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	componentID := seedProjectComponent(t, db, orgID, "p")
	scope := tenant.FromRun(db, orgID)

	score := 42
	if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, "made_up_future_kind",
		collector.Headline{Status: collector.StatusPass, Score: &score, Summary: "hypothetical"}); err != nil {
		t.Fatalf("upsert fictional kind: %v", err)
	}

	cm, err := repo.GetComponentMetric(context.Background(), scope, componentID)
	if err != nil {
		t.Fatalf("GetComponentMetric: %v", err)
	}
	kh, ok := cm.Metrics()["made_up_future_kind"]
	if !ok || kh.Summary != "hypothetical" {
		t.Fatalf("metrics map = %+v, want the fictional kind readable with no schema change", cm.Metrics())
	}
}

// countingDriver wraps pgx's stdlib driver, counting every QueryContext/
// ExecContext call across every connection it opens — the mechanism
// TestListComponentMetricsForProject_OneQueryForFiftyComponents uses to
// prove "one query" the way task 3.6's own acceptance criterion demands:
// "dibuktikan lewat query counter, bukan lewat pengamatan" (proven via a
// query counter, not observation).
type countingDriver struct {
	driver.Driver
	count *int64
}

func (d countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{conn, d.count}, nil
}

type countingConn struct {
	driver.Conn
	count *int64
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	atomic.AddInt64(c.count, 1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	atomic.AddInt64(c.count, 1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

// countingConn must also implement Prepare/Close/Begin (embedded via
// driver.Conn) and, since sqlx/database-sql prefer context-aware
// prepares, BeginTx/PrepareContext — driver.Conn's embedded methods
// cover Prepare/Close/Begin; pgx's stdlib conn also implements
// ConnBeginTx and ConnPrepareContext, which Go's database/sql will use
// via type assertion on the returned driver.Conn if present. Since
// countingConn wraps (embeds) the real conn, those assertions fail
// unless forwarded explicitly.
func (c countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func openCountingDB(t *testing.T, dsn string) (*sqlx.DB, *int64) {
	t.Helper()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	baseDriver := base.Driver()
	base.Close()

	count := new(int64)
	driverName := "pgx-counting-" + uuid.NewString()
	sql.Register(driverName, countingDriver{Driver: baseDriver, count: count})

	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open counting db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlx.NewDb(sqlDB, "pgx"), count
}

// TestListComponentMetricsForProject_OneQueryForFiftyComponents is task
// 3.6's own acceptance criterion: a dashboard of 50 components x 4
// providers renders with one query to component_metrics_current.
func TestListComponentMetricsForProject_OneQueryForFiftyComponents(t *testing.T) {
	dsn := dbtest.DSN(t)
	setupDB, count := openCountingDB(t, dsn)

	var orgID uuid.UUID
	if err := setupDB.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	scope := tenant.FromRun(setupDB, orgID)

	var projectID uuid.UUID
	withOrgTxCM(t, setupDB, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	})

	const kinds = "gitlab,sonarqube,trivy,sentry"
	for i := range 50 {
		componentID := seedProjectComponentInProject(t, setupDB, orgID, projectID, i)
		for _, kind := range splitKinds(kinds) {
			score := 80
			if err := repo.UpsertComponentMetric(context.Background(), scope, componentID, kind,
				collector.Headline{Status: collector.StatusPass, Score: &score}); err != nil {
				t.Fatalf("seed upsert: %v", err)
			}
		}
	}

	// Reset the counter after seeding — only the read under test counts.
	atomic.StoreInt64(count, 0)

	rows, err := repo.ListComponentMetricsForProject(context.Background(), scope, projectID)
	if err != nil {
		t.Fatalf("ListComponentMetricsForProject: %v", err)
	}
	if len(rows) != 50 {
		t.Fatalf("got %d components, want 50", len(rows))
	}
	// 2, not 1: tenant.Scope.Tx (internal/tenant/scope.go) issues one
	// `SELECT set_config('app.org_id', ...)` before every scoped query as
	// RLS plumbing — fixed overhead present on every single Scope-backed
	// call anywhere in this codebase, not something specific to this read.
	// The acceptance criterion this test is proving is "no per-provider
	// subqueries" (the legacy dashboard's own five separate MAX(id) GROUP
	// BY queries per request) — one real SELECT against
	// component_metrics_current, not one wire round-trip.
	if got := atomic.LoadInt64(count); got != 2 {
		t.Fatalf("query count = %d, want exactly 2 (1 RLS set_config + 1 SELECT — no per-provider subqueries)", got)
	}
}

func seedProjectComponentInProject(t *testing.T, db *sqlx.DB, orgID, projectID uuid.UUID, i int) uuid.UUID {
	t.Helper()
	var componentID uuid.UUID
	withOrgTxCM(t, db, orgID, func(tx *sqlx.Tx) {
		slug := "c" + uuid.NewString()[:8]
		_ = i
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $3, 'backend') RETURNING id`,
			orgID, projectID, slug); err != nil {
			t.Fatalf("insert component: %v", err)
		}
	})
	return componentID
}

func splitKinds(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
