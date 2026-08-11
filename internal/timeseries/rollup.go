package timeseries

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

// RollupHourly aggregates every metric_facts row within the one-hour
// bucket starting at hourStart (truncated to the hour) into
// metric_facts_hourly. Idempotent: running it twice for the same hour
// recomputes and overwrites the same aggregate from the same source
// rows, via ON CONFLICT DO UPDATE — it never adds a second row nor
// shifts an existing one, since the aggregate is a pure function of the
// (unchanging, once the hour has passed) raw rows in that window.
func RollupHourly(ctx context.Context, db *sqlx.DB, hourStart time.Time) error {
	return rollupBucket(ctx, db, "metric_facts_hourly", hourStart.Truncate(time.Hour), time.Hour)
}

// RollupDaily aggregates every metric_facts row within the one-day
// bucket starting at dayStart (truncated to midnight UTC) into
// metric_facts_daily. Aggregates directly from the raw tier — not from
// the hourly tier — so this always matches a direct aggregation over
// raw exactly (task 3.7's own acceptance criterion), with no
// average-of-averages weighting to get right. This is safe because the
// daily job runs within a day of the data it's aggregating, and raw
// retention (14 days) comfortably outlives that lag — see this
// package's own doc comment on Run's ordering (roll up the period that
// just finished, never earlier than raw retention allows) and
// BackfillDaily for the same reasoning applied to a longer backfill.
func RollupDaily(ctx context.Context, db *sqlx.DB, dayStart time.Time) error {
	day := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 0, 0, 0, 0, time.UTC)
	return rollupBucket(ctx, db, "metric_facts_daily", day, 24*time.Hour)
}

// rollupBucket does the actual aggregation, shared by both tiers: same
// source table, same aggregate shape, only the target table and bucket
// width differ.
func rollupBucket(ctx context.Context, db *sqlx.DB, targetTable string, bucketStart time.Time, width time.Duration) error {
	bucketEnd := bucketStart.Add(width)
	q := `
		INSERT INTO ` + targetTable + ` (org_id, binding_id, component_id, key, bucket, value_min, value_max, value_avg, value_last, sample_count)
		SELECT org_id, binding_id, component_id, key, $1::timestamptz AS bucket,
		       MIN(value_num), MAX(value_num), AVG(value_num),
		       (ARRAY_AGG(value_num ORDER BY collected_at DESC))[1],
		       COUNT(*)
		FROM metric_facts
		WHERE collected_at >= $1 AND collected_at < $2 AND value_num IS NOT NULL
		GROUP BY org_id, binding_id, component_id, key
		ON CONFLICT (binding_id, key, bucket) DO UPDATE SET
			value_min = EXCLUDED.value_min, value_max = EXCLUDED.value_max,
			value_avg = EXCLUDED.value_avg, value_last = EXCLUDED.value_last,
			sample_count = EXCLUDED.sample_count`
	_, err := db.ExecContext(ctx, q, bucketStart, bucketEnd)
	return err
}

// BackfillHourly rolls up every hour in [from, to) — task 3.7's own
// requirement that the rollup tiers can be introduced after raw data
// already exists, not only maintained incrementally from here on.
func BackfillHourly(ctx context.Context, db *sqlx.DB, from, to time.Time) error {
	for h := from.Truncate(time.Hour); h.Before(to); h = h.Add(time.Hour) {
		if err := RollupHourly(ctx, db, h); err != nil {
			return err
		}
	}
	return nil
}

// BackfillDaily rolls up every day in [from, to).
func BackfillDaily(ctx context.Context, db *sqlx.DB, from, to time.Time) error {
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for d := start; d.Before(to); d = d.AddDate(0, 0, 1) {
		if err := RollupDaily(ctx, db, d); err != nil {
			return err
		}
	}
	return nil
}
