// Package retention implements task 3.6's snapshot-table aging: creating
// future monthly partitions ahead of need, and dropping ones old enough
// to fall outside the retention window — DROP PARTITION being a
// catalog-only operation, not a row-by-row DELETE, which is the whole
// reason the four snapshot tables were partitioned by captured_at at all
// (migration 00018's own comment: the difference between a 3-second
// maintenance window and a vacuum incident).
//
// One window serves every organization: a partition holds every org's
// rows for that month, so honoring a per-org window here would mean
// either per-org partitioning or falling back to row-level DELETE for
// the orgs that differ — exactly the cost this design avoids. That one
// window is the longest organizations.retention_days (HeadlineAge), so
// the Settings value is honored for the organization that asked for the
// most and exceeded for the rest.
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	lotusdb "github.com/uyab/lotus/internal/db"
)

// snapshotTables is every table partitioned by migration 00018.
var snapshotTables = []string{"gitlab_snapshots", "sonar_snapshots", "trivy_snapshots", "sentry_snapshots"}

// tablesWithRawDetail is the subset that stores a raw upstream JSON blob
// worth nulling out separately from the row itself — sentry_snapshots
// has no such column (its "detail" is the sentry_issues rows, which
// aren't touched by this step; see DropOldPartitions's own comment on
// why they need their own explicit delete).
var tablesWithRawDetail = []string{"gitlab_snapshots", "sonar_snapshots", "trivy_snapshots"}

const (
	// DefaultRawDetailAge is task 3.6's "raw detail blob 30 hari" window.
	DefaultRawDetailAge = 30 * 24 * time.Hour
	// DefaultHeadlineAge is task 3.6's "headline resolusi penuh 90 hari"
	// window, used when no organization sets retention_days (see
	// HeadlineAge) — v1 drops the row outright at this age rather than rolling
	// it up to a daily aggregate first; task 3.7's own rollup machinery
	// is where that daily-rollup tier belongs once it exists (see this
	// package's own TENSION note, logged to ActiveCollab).
	DefaultHeadlineAge = 90 * 24 * time.Hour
	// DefaultSyncRunAge is task 3.6's "sync_runs 30 hari" window.
	DefaultSyncRunAge = 30 * 24 * time.Hour
	// DefaultPartitionMonthsAhead keeps this many future months' worth of
	// partitions always present, so a write never races partition
	// creation.
	DefaultPartitionMonthsAhead = 3
)

// Options configures Run's periodic loop.
type Options struct {
	Every                time.Duration
	RawDetailAge         time.Duration
	HeadlineAge          time.Duration
	SyncRunAge           time.Duration
	PartitionMonthsAhead int
}

func (o *Options) setDefaults() {
	if o.Every <= 0 {
		o.Every = time.Hour
	}
	if o.RawDetailAge <= 0 {
		o.RawDetailAge = DefaultRawDetailAge
	}
	if o.SyncRunAge <= 0 {
		o.SyncRunAge = DefaultSyncRunAge
	}
	if o.PartitionMonthsAhead <= 0 {
		o.PartitionMonthsAhead = DefaultPartitionMonthsAhead
	}
}

// HeadlineAge is how long snapshot rows are kept: the longest
// organizations.retention_days among live organizations (Settings →
// Organization), never less than DefaultHeadlineAge — a small number typed
// into Settings must not start deleting history. The longest, because a
// monthly partition holds every organization's rows (see this package's
// doc comment): an organization with a shorter window keeps more than it
// asked for, never less. Zero when it cannot be read: the caller skips
// dropping rather than guess.
func HeadlineAge(ctx context.Context, db *sqlx.DB) time.Duration {
	var days sql.NullInt64
	if err := db.GetContext(ctx, &days, `SELECT max(retention_days) FROM organizations WHERE deleted_at IS NULL`); err != nil {
		log.Printf("retention: read retention_days: %v", err)
		return 0
	}
	age := DefaultHeadlineAge
	if days.Valid && time.Duration(days.Int64)*24*time.Hour > age {
		age = time.Duration(days.Int64) * 24 * time.Hour
	}
	return age
}

// Run executes one retention pass immediately, then repeats every
// o.Every until ctx is cancelled. Safe to run in every replica: every
// statement here is idempotent (IF NOT EXISTS / IF EXISTS, or a WHERE
// clause that's already-satisfied on a second run), so two replicas
// racing each other lose nothing beyond a redundant DDL statement.
func Run(ctx context.Context, db *sqlx.DB, opts Options) {
	opts.setDefaults()
	if ok, err := lotusdb.SeesAllTenants(ctx, db); err != nil || !ok {
		log.Printf("retention: ERROR: this connection's role cannot read tenant rows through row-level security (it needs BYPASSRLS, as deploy/bootstrap.sh grants the migrate role); every pass will find nothing to do (err: %v)", err)
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
		log.Printf("retention: ensure partitions: %v", err)
	}
	if err := DropStaleRawDetail(ctx, db, now.Add(-opts.RawDetailAge)); err != nil {
		log.Printf("retention: drop stale raw detail: %v", err)
	}
	headlineAge := opts.HeadlineAge
	if headlineAge <= 0 {
		headlineAge = HeadlineAge(ctx, db)
	}
	switch {
	case headlineAge <= 0:
		// retention_days could not be read: dropping on a guessed window
		// can delete history that cannot be restored, so skip this pass.
	case lotusdb.LegacyImportRunning(ctx, db):
		log.Printf("retention: a legacy import is running; not dropping partitions this pass")
	default:
		if err := DropOldPartitions(ctx, db, now.Add(-headlineAge)); err != nil {
			log.Printf("retention: drop old partitions: %v", err)
		}
		if err := DeleteOldProjectStatusDays(ctx, db, now.Add(-headlineAge)); err != nil {
			log.Printf("retention: delete old project status days: %v", err)
		}
	}
	if err := DeleteOldSyncRuns(ctx, db, now.Add(-opts.SyncRunAge)); err != nil {
		log.Printf("retention: delete old sync_runs: %v", err)
	}
}

// partitionName mirrors migration 00018's own naming convention
// (`{table}_{YYYY}_{MM}`) — every partition this package creates or
// drops must resolve to exactly the same name goose's own initial
// partitions used, or the two schemes silently diverge into duplicate,
// overlapping, or gap'd ranges.
func partitionName(table string, month time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d", table, month.Year(), int(month.Month()))
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// EnsurePartitions creates any of the next monthsAhead months' partitions
// (starting from from's own month) that don't already exist, for every
// snapshot table — the write-side counterpart to DropOldPartitions.
// IF NOT EXISTS makes this safe to call unconditionally on every pass,
// not just when a new month actually starts.
func EnsurePartitions(ctx context.Context, db *sqlx.DB, from time.Time, monthsAhead int) error {
	start := monthStart(from)
	for _, table := range snapshotTables {
		for i := 0; i < monthsAhead; i++ {
			lower := start.AddDate(0, i, 0)
			upper := lower.AddDate(0, 1, 0)
			name := partitionName(table, lower)
			q := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				name, table, lower.Format("2006-01-02"), upper.Format("2006-01-02"),
			)
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("create partition %s: %w", name, err)
			}
		}
	}
	return nil
}

// DropStaleRawDetail nulls out the raw upstream JSON blob on rows older
// than cutoff, keeping every other column (the "headline" in task 3.6's
// own loose sense) intact — a component's detail page loses its raw CVE/
// pipeline/measures dump past this age, but its score/grade/counts stay
// exactly as collected.
func DropStaleRawDetail(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	for _, table := range tablesWithRawDetail {
		q := fmt.Sprintf(`UPDATE %s SET raw_json = NULL WHERE captured_at < $1 AND raw_json IS NOT NULL`, table)
		if _, err := db.ExecContext(ctx, q, cutoff); err != nil {
			return fmt.Errorf("drop stale raw detail on %s: %w", table, err)
		}
	}
	return nil
}

// DropOldPartitions drops every snapshot-table partition entirely below
// cutoff. For sentry_snapshots specifically, its sentry_issues rows are
// deleted first, explicitly, by snapshot_captured_at: sentry_issues has
// no FK to sentry_snapshots at all (migration 00018's own comment — a FK
// from another table makes every one of a partitioned table's partitions
// permanently undroppable), so nothing cascades here; an un-swept
// sentry_issues row would otherwise just sit there referencing a
// sentry_snapshots id/captured_at pair that no longer exists anywhere.
func DropOldPartitions(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	if err := deleteOldSentryIssues(ctx, db, cutoff); err != nil {
		return err
	}
	for _, table := range snapshotTables {
		names, err := partitionsBefore(ctx, db, table, cutoff)
		if err != nil {
			return fmt.Errorf("list partitions of %s: %w", table, err)
		}
		for _, name := range names {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)); err != nil {
				return fmt.Errorf("drop partition %s: %w", name, err)
			}
		}
	}
	return nil
}

// deleteOldSentryIssues removes issues belonging to snapshots about to
// age out, ahead of the partition drop that would otherwise orphan them.
func deleteOldSentryIssues(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sentry_issues WHERE snapshot_captured_at < $1`, cutoff)
	return err
}

// partitionsBefore returns the names of table's partitions whose entire
// range falls before cutoff, by inspecting pg_inherits/pg_class — never
// a partition straddling cutoff, so a row just inside the retention
// window is never dropped early.
func partitionsBefore(ctx context.Context, db *sqlx.DB, table string, cutoff time.Time) ([]string, error) {
	rows, err := db.QueryxContext(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`, table)
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
	var out []string
	for _, name := range all {
		month, ok := monthFromPartitionName(table, name)
		if !ok {
			continue
		}
		// A partition covers [month, month+1); it's entirely before
		// cutoff only once that whole range has passed.
		if month.AddDate(0, 1, 0).Before(cutoffMonth) || month.AddDate(0, 1, 0).Equal(cutoffMonth) {
			out = append(out, name)
		}
	}
	return out, nil
}

func monthFromPartitionName(table, name string) (time.Time, bool) {
	prefix := table + "_"
	if len(name) != len(prefix)+7 || name[:len(prefix)] != prefix {
		return time.Time{}, false
	}
	t, err := time.Parse("2006_01", name[len(prefix):])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DeleteOldSyncRuns removes sync_runs rows older than cutoff — task
// 3.6's "sync_runs 30 hari" window. sync_runs is deliberately unscoped
// (task 3.2's own migration comment), so this is a plain, org-agnostic
// DELETE, not a tenant.Scope operation.
func DeleteOldSyncRuns(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sync_runs WHERE created_at < $1`, cutoff)
	return err
}

// DeleteOldProjectStatusDays removes every organization's recorded status
// days (the public status page's history) before cutoff's date. They are
// kept as long as the snapshots they were recorded alongside, so a day
// past retention reads "No data" on the page.
func DeleteOldProjectStatusDays(ctx context.Context, db *sqlx.DB, cutoff time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM project_status_days WHERE day < $1::date`, cutoff.Format(time.DateOnly))
	return err
}
