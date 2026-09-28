// Package timeseries implements task 3.7's background machinery: rolling
// raw metric_facts up into the hourly and daily tiers, aging each of the
// three tiers out on its own schedule, and freezing a metric_keys row to
// active=false once its key stops being emitted. internal/repo owns the
// read/write paths (Series, InsertMetricFacts) this package's output
// feeds; this package owns everything that runs on a clock instead of a
// request.
package timeseries

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	lotusdb "github.com/uyab/lotus/internal/db"
)

const (
	// DefaultRawRetention is task 3.7's "14 hari" window for metric_facts.
	DefaultRawRetention = 14 * 24 * time.Hour
	// DefaultHourlyRetention is task 3.7's "45 hari" window.
	DefaultHourlyRetention = 45 * 24 * time.Hour
	// DefaultDailyRetention is task 3.7's "400 hari" window (v1's stand-in
	// for organizations.retention_days — see internal/retention's own
	// TENSION note on why per-org windows aren't honored at the
	// partition/bulk-delete level yet; the same reasoning applies here).
	DefaultDailyRetention = 400 * 24 * time.Hour
	// DefaultKeyStaleness is how long a metric_keys row can go without a
	// fresh fact before the staleness sweep marks it active=false. Set
	// well above any collector's DefaultInterval (the longest today is
	// trivy's 1 hour) so a binding that's merely due-but-not-yet-run, or
	// briefly failing, is never mistaken for a metric that was
	// discontinued.
	DefaultKeyStaleness = 72 * time.Hour
	// DefaultPartitionMonthsAhead mirrors internal/retention's own constant.
	DefaultPartitionMonthsAhead = 3
)

const factsTable = "metric_facts"

// Options configures Run's periodic loop.
type Options struct {
	Every                time.Duration
	RawRetention         time.Duration
	HourlyRetention      time.Duration
	DailyRetention       time.Duration
	KeyStaleness         time.Duration
	PartitionMonthsAhead int
}

func (o *Options) setDefaults() {
	if o.Every <= 0 {
		o.Every = time.Hour
	}
	if o.RawRetention <= 0 {
		o.RawRetention = DefaultRawRetention
	}
	if o.HourlyRetention <= 0 {
		o.HourlyRetention = DefaultHourlyRetention
	}
	if o.DailyRetention <= 0 {
		o.DailyRetention = DefaultDailyRetention
	}
	if o.KeyStaleness <= 0 {
		o.KeyStaleness = DefaultKeyStaleness
	}
	if o.PartitionMonthsAhead <= 0 {
		o.PartitionMonthsAhead = DefaultPartitionMonthsAhead
	}
}

// Run executes one pass immediately, then repeats every o.Every until
// ctx is cancelled. Like internal/retention.Run, db must be a
// DDL-capable connection (partition create/drop) — see that package's
// own doc comment on lotus_app's deliberately narrow grants.
func Run(ctx context.Context, db *sqlx.DB, opts Options) {
	opts.setDefaults()
	if ok, err := lotusdb.SeesAllTenants(ctx, db); err != nil || !ok {
		log.Printf("timeseries: ERROR: this connection's role cannot read tenant rows through row-level security (it needs BYPASSRLS, as deploy/bootstrap.sh grants the migrate role); every pass will find nothing to do (err: %v)", err)
	}
	runOnce(ctx, db, opts)

	ticker := time.NewTicker(opts.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce(ctx, db, opts)
		}
	}
}

func runOnce(ctx context.Context, db *sqlx.DB, opts Options) {
	now := time.Now()

	if err := EnsurePartitions(ctx, db, now, opts.PartitionMonthsAhead); err != nil {
		log.Printf("timeseries: ensure partitions: %v", err)
	}

	// Roll up the hour/day that just finished — never the current,
	// still-open one, so a rollup is always computed from a complete set
	// of raw rows and stays idempotent under repeated runs.
	lastHour := now.Truncate(time.Hour).Add(-time.Hour)
	if err := RollupHourly(ctx, db, lastHour); err != nil {
		log.Printf("timeseries: rollup hourly: %v", err)
	}
	yesterday := truncateToDay(now).AddDate(0, 0, -1)
	if err := RollupDaily(ctx, db, yesterday); err != nil {
		log.Printf("timeseries: rollup daily: %v", err)
	}

	if lotusdb.LegacyImportRunning(ctx, db) {
		// `lotus import-legacy` is writing history into past months'
		// partitions and rolls them up as it goes; dropping them now would
		// lose what it just wrote. The next pass after it ends catches up.
		log.Printf("timeseries: a legacy import is running; skipping this pass's aging")
		return
	}
	if err := DropOldFactPartitions(ctx, db, now.Add(-opts.RawRetention)); err != nil {
		log.Printf("timeseries: drop old fact partitions: %v", err)
	}
	if err := DeleteOldRollups(ctx, db, "metric_facts_hourly", now.Add(-opts.HourlyRetention)); err != nil {
		log.Printf("timeseries: delete old hourly rollups: %v", err)
	}
	if err := DeleteOldRollups(ctx, db, "metric_facts_daily", now.Add(-opts.DailyRetention)); err != nil {
		log.Printf("timeseries: delete old daily rollups: %v", err)
	}
	if err := DeactivateStaleMetricKeys(ctx, db, now.Add(-opts.KeyStaleness)); err != nil {
		log.Printf("timeseries: deactivate stale metric keys: %v", err)
	}
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.UTC().Location())
}

// ─── Partitions (metric_facts only — the two rollup tables aren't partitioned) ───

func partitionName(month time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d", factsTable, month.Year(), int(month.Month()))
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// EnsurePartitions creates any of metric_facts' next monthsAhead months'
// partitions that don't already exist — see
// internal/retention.EnsurePartitions's own doc comment; this is that
// same operation, scoped to the one table this package owns.
func EnsurePartitions(ctx context.Context, db *sqlx.DB, from time.Time, monthsAhead int) error {
	start := monthStart(from)
	for i := 0; i < monthsAhead; i++ {
		lower := start.AddDate(0, i, 0)
		upper := lower.AddDate(0, 1, 0)
		name := partitionName(lower)
		q := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
			name, factsTable, lower.Format("2006-01-02"), upper.Format("2006-01-02"),
		)
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("create partition %s: %w", name, err)
		}
	}
	return nil
}

// DropOldFactPartitions drops every metric_facts partition entirely
// below cutoff — a catalog-only operation, the reason this table is
// partitioned at all given the spec's own volume estimate (±190k rows/
// day/org at the raw tier).
func DropOldFactPartitions(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	names, err := partitionsBefore(ctx, db, cutoff)
	if err != nil {
		return fmt.Errorf("list partitions: %w", err)
	}
	for _, name := range names {
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)); err != nil {
			return fmt.Errorf("drop partition %s: %w", name, err)
		}
	}
	return nil
}

func partitionsBefore(ctx context.Context, db *sqlx.DB, cutoff time.Time) ([]string, error) {
	rows, err := db.QueryxContext(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`, factsTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		all = append(all, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cutoffMonth := monthStart(cutoff)
	prefix := factsTable + "_"
	var out []string
	for _, name := range all {
		if len(name) != len(prefix)+7 || name[:len(prefix)] != prefix {
			continue
		}
		month, err := time.Parse("2006_01", name[len(prefix):])
		if err != nil {
			continue
		}
		if !month.AddDate(0, 1, 0).After(cutoffMonth) {
			out = append(out, name)
		}
	}
	return out, nil
}

// DeleteOldRollups removes rows older than cutoff from a rollup table —
// a plain bulk DELETE is fine here (unlike the raw tier): these tables
// hold one row per binding+key+bucket, orders of magnitude smaller than
// raw, so there's no vacuum-incident risk to design around.
func DeleteOldRollups(ctx context.Context, db *sqlx.DB, table string, cutoff time.Time) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE bucket < $1`, table)
	_, err := db.ExecContext(ctx, q, cutoff)
	return err
}

// DeactivateStaleMetricKeys marks active=false for any catalog row whose
// last_seen_at is older than cutoff — task 3.7's own rule that a
// discontinued metric's row is frozen, never deleted, so its history
// stays queryable and the UI can tell "stopped being collected" apart
// from "never had data".
func DeactivateStaleMetricKeys(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE metric_keys SET active = false WHERE active = true AND last_seen_at < $1`, cutoff)
	return err
}
