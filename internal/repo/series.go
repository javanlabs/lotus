package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// Point is one sample of a Series — task 3.7's own type, quoted
// verbatim. HasValue is the whole reason this isn't just a bare float:
// an empty bucket (a rarely-measured metric between collects) must never
// be indistinguishable from a real, measured zero.
type Point struct {
	At       time.Time
	Value    float64
	HasValue bool
	Min, Max float64
}

// Tier range thresholds: a query spanning up to rawTierMax reads the raw
// tier, up to hourlyTierMax reads hourly, beyond that reads daily. Set
// with headroom over the three nominal ranges (7 days / 1 month / 1
// year) so a caller asking for "exactly 7 days" or "exactly 1 month"
// still lands on the tier meant for it rather than tipping into the
// next one on a rounding edge.
const (
	rawTierMax    = 9 * 24 * time.Hour
	hourlyTierMax = 40 * 24 * time.Hour
)

// Series returns key's values for bindingID over [from, to], reading
// whichchever of the three tiers (raw / hourly / daily) fits the range
// width — the caller never names a table. Task 3.7's acceptance
// criterion is specifically that this tier choice is proven by a query
// counter, not by reading the code: see series_test.go.
func Series(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, key string, from, to time.Time) ([]Point, error) {
	width := to.Sub(from)
	switch {
	case width <= rawTierMax:
		return seriesRaw(ctx, s, bindingID, key, from, to)
	case width <= hourlyTierMax:
		return seriesBucketed(ctx, s, "metric_facts_hourly", time.Hour, bindingID, key, from, to)
	default:
		return seriesBucketed(ctx, s, "metric_facts_daily", 24*time.Hour, bindingID, key, from, to)
	}
}

// seriesRaw returns one Point per metric_facts row — each collect is
// already a distinct, real point, so there's no bucket grid to fill in;
// a gap here just means fewer points, exactly reflecting an irregular
// collect schedule.
func seriesRaw(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, key string, from, to time.Time) ([]Point, error) {
	var rows []struct {
		CollectedAt time.Time `db:"collected_at"`
		ValueNum    float64   `db:"value_num"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT collected_at, value_num FROM metric_facts
			WHERE binding_id = $1 AND key = $2 AND value_num IS NOT NULL AND collected_at >= $3 AND collected_at <= $4
			ORDER BY collected_at`, bindingID, key, from, to)
	})
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(rows))
	for _, r := range rows {
		out = append(out, Point{At: r.CollectedAt, Value: r.ValueNum, HasValue: true, Min: r.ValueNum, Max: r.ValueNum})
	}
	return out, nil
}

// seriesBucketed reads a rollup tier, left-joined against a
// generate_series grid spanning every bucket in [from, to] — a bucket
// with no rollup row still comes back as its own Point, with
// HasValue=false, at the correct timestamp, rather than being absent
// from the result entirely (task 3.7's own explicit requirement: an
// empty bucket rendered as 0 reads as an incident that never happened).
func seriesBucketed(ctx context.Context, s tenant.Scope, table string, bucketWidth time.Duration, bindingID uuid.UUID, key string, from, to time.Time) ([]Point, error) {
	var rows []struct {
		Bucket    time.Time `db:"bucket"`
		HasValue  bool      `db:"has_value"`
		ValueLast float64   `db:"value_last"`
		ValueMin  float64   `db:"value_min"`
		ValueMax  float64   `db:"value_max"`
	}
	interval, truncUnit := "1 hour", "hour"
	if bucketWidth == 24*time.Hour {
		interval, truncUnit = "1 day", "day"
	}
	q := `
		SELECT g.bucket,
		       (f.bucket IS NOT NULL) AS has_value,
		       COALESCE(f.value_last, 0) AS value_last,
		       COALESCE(f.value_min, 0) AS value_min,
		       COALESCE(f.value_max, 0) AS value_max
		FROM generate_series(date_trunc('` + truncUnit + `', $1::timestamptz), $2::timestamptz, ($5)::interval) AS g(bucket)
		LEFT JOIN ` + table + ` f ON f.bucket = g.bucket AND f.binding_id = $3 AND f.key = $4
		ORDER BY g.bucket`
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, q, from, to, bindingID, key, interval)
	})
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(rows))
	for _, r := range rows {
		out = append(out, Point{At: r.Bucket, Value: r.ValueLast, HasValue: r.HasValue, Min: r.ValueMin, Max: r.ValueMax})
	}
	return out, nil
}

// DailySeries is key's value on each of the last `days` calendar days up
// to and including now's: one Point per day, in order, the day's last
// reading. A day with no reading carries the previous day's value
// forward — the integrations report a state, and a state with no new
// reading has not changed — starting from the last reading before the
// window when there is one. Only days before the very first reading
// have no value. Days are the database's calendar days, the same buckets
// metric_facts_daily rolls up into; a day still in raw metric_facts
// (kept for 14) reads from there, an older one from the rollup.
func DailySeries(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, key string, days int, now time.Time) ([]Point, error) {
	if days < 1 {
		days = 1
	}
	var rows []struct {
		Day   time.Time `db:"day"`
		Value *float64  `db:"value"`
	}
	var seed *float64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &rows, `
			WITH bounds AS (
			    SELECT date_trunc('day', $3::timestamptz) - ($4::int - 1) * interval '1 day' AS first_day,
			           $3::timestamptz AS until
			), grid AS (
			    SELECT generate_series(b.first_day, date_trunc('day', b.until), interval '1 day') AS day FROM bounds b
			), raw AS (
			    SELECT DISTINCT ON (date_trunc('day', f.collected_at)) date_trunc('day', f.collected_at) AS day, f.value_num AS value
			    FROM metric_facts f, bounds b
			    WHERE f.binding_id = $1 AND f.key = $2 AND f.value_num IS NOT NULL
			      AND f.collected_at >= b.first_day AND f.collected_at <= b.until
			    ORDER BY date_trunc('day', f.collected_at), f.collected_at DESC
			), rolled AS (
			    SELECT d.bucket AS day, d.value_last AS value
			    FROM metric_facts_daily d, bounds b
			    WHERE d.binding_id = $1 AND d.key = $2 AND d.value_last IS NOT NULL AND d.bucket >= b.first_day
			)
			SELECT g.day, COALESCE(raw.value, rolled.value) AS value
			FROM grid g LEFT JOIN raw ON raw.day = g.day LEFT JOIN rolled ON rolled.day = g.day
			ORDER BY g.day`, bindingID, key, now, days); err != nil {
			return err
		}
		// The value going into the window: the newest reading before it,
		// raw or rolled up, whichever is later.
		return tx.GetContext(ctx, &seed, `
			SELECT value FROM (
			    (SELECT collected_at AS at, value_num AS value FROM metric_facts
			     WHERE binding_id = $1 AND key = $2 AND value_num IS NOT NULL AND collected_at < $3
			     ORDER BY collected_at DESC LIMIT 1)
			    UNION ALL
			    (SELECT bucket AS at, value_last AS value FROM metric_facts_daily
			     WHERE binding_id = $1 AND key = $2 AND value_last IS NOT NULL AND bucket < $3
			     ORDER BY bucket DESC LIMIT 1)
			) prior ORDER BY at DESC LIMIT 1`, bindingID, key, rows[0].Day)
	})
	if err != nil && !isNoRows(err) {
		return nil, err
	}
	out := make([]Point, 0, len(rows))
	carry := seed
	for _, r := range rows {
		if r.Value != nil {
			carry = r.Value
		}
		p := Point{At: r.Day}
		if carry != nil {
			p.Value, p.HasValue, p.Min, p.Max = *carry, true, *carry, *carry
		}
		out = append(out, p)
	}
	return out, nil
}
