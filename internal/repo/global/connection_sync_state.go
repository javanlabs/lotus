package global

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// SetConnectionRateLimited records a 429's Retry-After — task 3.1's rule
// that a rate-limited connection freezes for *every* binding and every
// worker, not just the one that hit the 429. Upserted so the first 429 a
// connection ever sees doesn't need a pre-existing sidecar row.
func SetConnectionRateLimited(ctx context.Context, db *sqlx.DB, connectionID uuid.UUID, until time.Time) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO connection_sync_state (connection_id, rate_limited_until) VALUES ($1, $2)
		ON CONFLICT (connection_id) DO UPDATE SET rate_limited_until = $2`, connectionID, until)
	return err
}

// SetConnectionBackoff records an upstream/timeout failure's backoff and
// bumps the circuit-breaker counter; RecordConnectionSuccess resets both.
func SetConnectionBackoff(ctx context.Context, db *sqlx.DB, connectionID uuid.UUID, until time.Time) (consecutiveFailures int, err error) {
	err = db.GetContext(ctx, &consecutiveFailures, `
		INSERT INTO connection_sync_state (connection_id, backoff_until, consecutive_upstream_failures)
		VALUES ($1, $2, 1)
		ON CONFLICT (connection_id) DO UPDATE
		SET backoff_until = $2, consecutive_upstream_failures = connection_sync_state.consecutive_upstream_failures + 1
		RETURNING consecutive_upstream_failures`, connectionID, until)
	return consecutiveFailures, err
}

func RecordConnectionSuccess(ctx context.Context, db *sqlx.DB, connectionID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO connection_sync_state (connection_id, backoff_until, consecutive_upstream_failures)
		VALUES ($1, NULL, 0)
		ON CONFLICT (connection_id) DO UPDATE SET backoff_until = NULL, consecutive_upstream_failures = 0`, connectionID)
	return err
}

type ConnectionSyncState struct {
	ConnectionID                uuid.UUID  `db:"connection_id"`
	RateLimitedUntil            *time.Time `db:"rate_limited_until"`
	BackoffUntil                *time.Time `db:"backoff_until"`
	ConsecutiveUpstreamFailures int        `db:"consecutive_upstream_failures"`
}

func GetConnectionSyncState(ctx context.Context, db *sqlx.DB, connectionID uuid.UUID) (*ConnectionSyncState, error) {
	var s ConnectionSyncState
	err := db.GetContext(ctx, &s, `
		SELECT connection_id, rate_limited_until, backoff_until, consecutive_upstream_failures
		FROM connection_sync_state WHERE connection_id = $1`, connectionID)
	if err != nil {
		if isNoRowsGlobal(err) {
			return &ConnectionSyncState{ConnectionID: connectionID}, nil
		}
		return nil, err
	}
	return &s, nil
}
