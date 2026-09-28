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
