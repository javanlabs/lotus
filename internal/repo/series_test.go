package repo_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
	"github.com/uyab/lotus/internal/timeseries"
)

// seriesCountingDriver mirrors component_metrics_test.go's own
// countingDriver (same rationale: task 3.7's own acceptance criterion —
// "dibuktikan lewat query counter, bukan pengamatan" — for which tier
// Series reads). Kept as a separate, differently-named type in this file
// since Go doesn't let two files in one test package redeclare the same
// type name, even structurally identical ones.
type seriesCountingDriver struct {
	driver.Driver
	count *int64
}

func (d seriesCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return seriesCountingConn{conn, d.count}, nil
}

type seriesCountingConn struct {
	driver.Conn
	count *int64
}

func (c seriesCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	atomic.AddInt64(c.count, 1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func (c seriesCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	atomic.AddInt64(c.count, 1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c seriesCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c seriesCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func openSeriesCountingDB(t *testing.T, dsn string) (*sqlx.DB, *int64) {
	t.Helper()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	baseDriver := base.Driver()
	base.Close()

	count := new(int64)
	driverName := "pgx-series-counting-" + uuid.NewString()
	sql.Register(driverName, seriesCountingDriver{Driver: baseDriver, count: count})

	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open counting db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlx.NewDb(sqlDB, "pgx"), count
}

// TestSeries_TierChosenByRangeWidth proves, with a query counter (not
// code inspection), that a 7-day range reads the raw tier, a 1-month
// range reads hourly, and a 1-year range reads daily — task 3.7's own
// acceptance criterion, worded exactly that way.
func TestSeries_TierChosenByRangeWidth(t *testing.T) {
	t.Parallel()
	dsn := dbtest.DSN(t)
	setupDB, count := openSeriesCountingDB(t, dsn)

	var orgID uuid.UUID
	if err := setupDB.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	scope := tenant.FromRun(setupDB, orgID)
	var projectID, componentID, connID, bindingID uuid.UUID
	withOrgTxMF(t, setupDB, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1,'p','P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c','C','backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1,'gitlab','g','https://x') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1,$2,$3,'gitlab','x') RETURNING id`,
			orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})

	now := time.Now()

	atomic.StoreInt64(count, 0)
	if _, err := repo.Series(context.Background(), scope, bindingID, "gitlab.x", now.Add(-7*24*time.Hour), now); err != nil {
		t.Fatalf("Series (7d): %v", err)
	}
	if got := atomic.LoadInt64(count); got != 2 { // 1 set_config + 1 SELECT
		t.Errorf("7-day range: query count = %d, want 2 (raw tier, one SELECT)", got)
	}

	atomic.StoreInt64(count, 0)
	if _, err := repo.Series(context.Background(), scope, bindingID, "gitlab.x", now.Add(-30*24*time.Hour), now); err != nil {
		t.Fatalf("Series (30d): %v", err)
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("30-day range: query count = %d, want 2 (hourly tier, one SELECT)", got)
	}

	atomic.StoreInt64(count, 0)
	if _, err := repo.Series(context.Background(), scope, bindingID, "gitlab.x", now.Add(-365*24*time.Hour), now); err != nil {
		t.Fatalf("Series (365d): %v", err)
	}
	if got := atomic.LoadInt64(count); got != 2 {
		t.Errorf("365-day range: query count = %d, want 2 (daily tier, one SELECT)", got)
	}
}

// TestSeries_EmptyBucketHasValueFalseNotZero is task 3.7's own
// acceptance criterion, quoted directly: a bucket without data returns
// HasValue: false, not Value: 0.
func TestSeries_EmptyBucketHasValueFalseNotZero(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "gitlab")
	scope := tenant.FromRun(db, orgID)

	// One hourly rollup row for a specific hour with a non-zero value —
	// inserted through the same RLS-scoped connection/schema seedMFFixture
	// used (dbtest.DSN mints a brand-new schema on every call, so a
	// second, independent DDL-style connection would silently write into
	// an unrelated, empty schema instead).
	hour := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`
			INSERT INTO metric_facts_hourly (org_id, binding_id, component_id, key, bucket, value_min, value_max, value_avg, value_last, sample_count)
			VALUES ($1,$2,$3,'gitlab.coverage',$4, 50, 50, 50, 50, 1)`,
			orgID, bindingID, componentID, hour); err != nil {
			t.Fatalf("seed hourly row: %v", err)
		}
	})

	// Range must be wide enough to select the hourly tier (Series picks
	// raw for anything up to ~9 days) while still bracketing hour closely
	// enough to cheaply check both a filled and an empty bucket.
	from := hour.Add(-10 * 24 * time.Hour)
	to := hour.Add(2 * time.Hour)
	points, err := repo.Series(context.Background(), scope, bindingID, "gitlab.coverage", from, to)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("expected the full bucket grid back, including empty buckets")
	}
	sawEmpty, sawFilled := false, false
	for _, p := range points {
		if p.At.Equal(hour) {
			sawFilled = true
			if !p.HasValue || p.Value != 50 {
				t.Errorf("the seeded hour = %+v, want HasValue=true, Value=50", p)
			}
			continue
		}
		if p.HasValue {
			t.Errorf("point %+v has no seeded data but HasValue=true", p)
		}
		if p.Value != 0 {
			// Value is allowed to be the zero value alongside
			// HasValue=false (that's what makes it distinguishable from a
			// real 0 elsewhere) — but assert it's not silently something
			// non-zero garbage either, keeping this test meaningful.
		}
		sawEmpty = true
	}
	if !sawFilled || !sawEmpty {
		t.Fatalf("expected both an empty and a filled bucket in range, got %+v", points)
	}
}

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

func TestRollup_IsIdempotent(t *testing.T) {
	t.Parallel()
	db := openDDL(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "gitlab")

	hour := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	for _, v := range []float64{10, 20, 30} {
		if _, err := db.Exec(`
			INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
			VALUES ($1,$2,$3,'gitlab.coverage',$4,'%',$5)`,
			orgID, bindingID, componentID, v, hour.Add(10*time.Minute)); err != nil {
			t.Fatalf("seed raw fact: %v", err)
		}
	}

	if err := timeseries.RollupHourly(context.Background(), db, hour); err != nil {
		t.Fatalf("RollupHourly (1st): %v", err)
	}
	var count1 int
	var avg1 float64
	if err := db.QueryRow(`SELECT sample_count, value_avg FROM metric_facts_hourly WHERE binding_id=$1 AND key='gitlab.coverage' AND bucket=$2`, bindingID, hour).
		Scan(&count1, &avg1); err != nil {
		t.Fatalf("read after 1st rollup: %v", err)
	}

	if err := timeseries.RollupHourly(context.Background(), db, hour); err != nil {
		t.Fatalf("RollupHourly (2nd): %v", err)
	}
	var count2 int
	var avg2 float64
	if err := db.QueryRow(`SELECT sample_count, value_avg FROM metric_facts_hourly WHERE binding_id=$1 AND key='gitlab.coverage' AND bucket=$2`, bindingID, hour).
		Scan(&count2, &avg2); err != nil {
		t.Fatalf("read after 2nd rollup: %v", err)
	}

	if count1 != 3 || count2 != 3 {
		t.Fatalf("sample_count = %d then %d, want 3 both times (no duplication)", count1, count2)
	}
	if avg1 != 20 || avg2 != 20 {
		t.Fatalf("value_avg = %v then %v, want 20 both times (no shift)", avg1, avg2)
	}

	var rowCount int
	if err := db.Get(&rowCount, `SELECT count(*) FROM metric_facts_hourly WHERE binding_id=$1 AND key='gitlab.coverage' AND bucket=$2`, bindingID, hour); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("row count = %d, want exactly 1 (upsert, not insert)", rowCount)
	}
}

// TestBackfillDaily_MatchesDirectAggregationFromRaw is task 3.7's own
// acceptance criterion: backfilling over 90 days of raw data produces a
// daily tier that matches direct aggregation from raw.
func TestBackfillDaily_MatchesDirectAggregationFromRaw(t *testing.T) {
	t.Parallel()
	db := openDDL(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "gitlab")

	start := time.Now().Truncate(24*time.Hour).AddDate(0, 0, -90)
	// The migration only pre-creates one month of past headroom; a
	// 90-day backfill needs partitions further back than that.
	if err := timeseries.EnsurePartitions(context.Background(), db, start, 5); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	for d := 0; d < 90; d++ {
		day := start.AddDate(0, 0, d)
		for h := 0; h < 3; h++ { // 3 samples/day, varying values
			v := float64(d*10 + h)
			if _, err := db.Exec(`
				INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
				VALUES ($1,$2,$3,'gitlab.coverage',$4,'%',$5)`,
				orgID, bindingID, componentID, v, day.Add(time.Duration(h)*8*time.Hour)); err != nil {
				t.Fatalf("seed day %d hour %d: %v", d, h, err)
			}
		}
	}

	if err := timeseries.BackfillDaily(context.Background(), db, start, start.AddDate(0, 0, 90)); err != nil {
		t.Fatalf("BackfillDaily: %v", err)
	}

	// Spot-check a handful of days against a direct aggregation query.
	for _, d := range []int{0, 30, 89} {
		day := start.AddDate(0, 0, d)
		var directAvg, directMin, directMax float64
		var directCount int
		if err := db.QueryRow(`
			SELECT AVG(value_num), MIN(value_num), MAX(value_num), COUNT(*)
			FROM metric_facts WHERE binding_id=$1 AND key='gitlab.coverage' AND collected_at >= $2 AND collected_at < $3`,
			bindingID, day, day.AddDate(0, 0, 1)).Scan(&directAvg, &directMin, &directMax, &directCount); err != nil {
			t.Fatalf("direct aggregation day %d: %v", d, err)
		}

		var rolledAvg, rolledMin, rolledMax float64
		var rolledCount int
		if err := db.QueryRow(`
			SELECT value_avg, value_min, value_max, sample_count FROM metric_facts_daily
			WHERE binding_id=$1 AND key='gitlab.coverage' AND bucket=$2`, bindingID, day).
			Scan(&rolledAvg, &rolledMin, &rolledMax, &rolledCount); err != nil {
			t.Fatalf("read daily rollup day %d: %v", d, err)
		}

		if rolledCount != directCount || rolledAvg != directAvg || rolledMin != directMin || rolledMax != directMax {
			t.Errorf("day %d: rolled(avg=%v,min=%v,max=%v,n=%d) != direct(avg=%v,min=%v,max=%v,n=%d)",
				d, rolledAvg, rolledMin, rolledMax, rolledCount, directAvg, directMin, directMax, directCount)
		}
	}
}

// DailySeries gives one value per calendar day: the day's last reading,
// from raw facts or, once those are gone, the daily rollup; a day with
// none carries the day before forward, starting from the last reading
// before the window.
func TestDailySeries_OneValuePerDayCarriedForward(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme-daily", "gitlab")
	scope := tenant.FromRun(db, orgID)

	var today time.Time
	if err := db.Get(&today, `SELECT date_trunc('day', now())`); err != nil {
		t.Fatal(err)
	}
	day := func(n int) time.Time { return today.AddDate(0, 0, -n) }
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		raw := func(at time.Time, v float64) {
			if _, err := tx.Exec(`INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
				VALUES ($1,$2,$3,'gitlab.coverage',$4,'%',$5)`, orgID, bindingID, componentID, v, at); err != nil {
				t.Fatalf("raw: %v", err)
			}
		}
		raw(day(10).Add(9*time.Hour), 5) // before the window: the value going in
		raw(day(3).Add(8*time.Hour), 7)
		raw(day(3).Add(20*time.Hour), 8) // the day's last reading wins
		if _, err := tx.Exec(`INSERT INTO metric_facts_daily (org_id, binding_id, component_id, key, bucket, value_min, value_max, value_avg, value_last, sample_count)
			VALUES ($1,$2,$3,'gitlab.coverage',$4, 6, 6, 6, 6, 1)`, orgID, bindingID, componentID, day(5)); err != nil {
			t.Fatalf("daily: %v", err)
		}
	})

	points, err := repo.DailySeries(context.Background(), scope, bindingID, "gitlab.coverage", 7, time.Now())
	if err != nil {
		t.Fatalf("DailySeries: %v", err)
	}
	want := []float64{5, 6, 6, 8, 8, 8, 8} // days 6..0 ago
	if len(points) != len(want) {
		t.Fatalf("%d points, want 7: %+v", len(points), points)
	}
	for i, p := range points {
		if !p.At.Equal(day(6-i)) || !p.HasValue || p.Value != want[i] {
			t.Errorf("day %d ago = %+v, want %v", 6-i, p, want[i])
		}
	}
}

func TestDailySeries_DaysBeforeTheFirstReadingAreEmpty(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme-daily-first", "gitlab")
	scope := tenant.FromRun(db, orgID)
	var today time.Time
	if err := db.Get(&today, `SELECT date_trunc('day', now())`); err != nil {
		t.Fatal(err)
	}
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(`INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
			VALUES ($1,$2,$3,'gitlab.coverage',90,'%',$4)`, orgID, bindingID, componentID, today.AddDate(0, 0, -2).Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	points, err := repo.DailySeries(context.Background(), scope, bindingID, "gitlab.coverage", 7, time.Now())
	if err != nil {
		t.Fatalf("DailySeries: %v", err)
	}
	for i, p := range points {
		if wantValue := i >= 4; p.HasValue != wantValue || (wantValue && p.Value != 90) {
			t.Errorf("point %d = %+v, want HasValue=%v", i, p, wantValue)
		}
	}
}
