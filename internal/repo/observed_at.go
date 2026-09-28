package repo

import (
	"context"
	"time"
)

type observedAtKey struct{}

// WithObservedAt marks the snapshot and fact writes made under ctx as
// observations made at t rather than now — a historical import
// (`lotus import-legacy`). Snapshots are captured and collected at t and
// facts collected at t; write-dedupe compares against the binding's
// history up to t only, so a live row is never touched with an old time;
// and the dashboard's current state (component_metrics_current) is left
// to live collection. Live collection never sets it.
func WithObservedAt(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, observedAtKey{}, t)
}

// observedAt is the time WithObservedAt set on ctx, if any.
func observedAt(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(observedAtKey{}).(time.Time)
	return t, ok
}

type metricKeyMemoKey struct{}

// WithMetricKeyMemo lets a long run of InsertMetricFacts calls (an
// import) seed and upsert each metric_keys row once instead of on every
// call — hundreds of thousands of updates of the same few rows otherwise,
// contending with live collection. Not safe for concurrent use.
func WithMetricKeyMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, metricKeyMemoKey{}, map[string]bool{})
}

// metricKeyOnce reports whether key is the first sighting under ctx's
// memo, recording it; always true without a memo.
func metricKeyOnce(ctx context.Context, key string) bool {
	memo, ok := ctx.Value(metricKeyMemoKey{}).(map[string]bool)
	if !ok {
		return true
	}
	if memo[key] {
		return false
	}
	memo[key] = true
	return true
}
