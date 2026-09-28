package repo

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/tenant"
)

// snapshotWrite is the shared machinery every one-row-per-collect kind's
// own InsertXSnapshot delegates to (gitlab, sonarqube, trivy — sentry's
// own multi-row-per-collect shape needs its own version, see
// sentry_snapshots.go). Within one transaction: check table's most
// recent row for bindingID against contentHash — task 3.6's write-dedupe
// rule — and on a match just bump collected_at and skip insertFn
// entirely; otherwise run insertFn to write the fresh row. Either way,
// finish by upserting this component's dashboard projection row, so it
// always lands atomically with whichever branch ran, per task 3.6's own
// "upserted in the same transaction as the snapshot write" requirement.
func snapshotWrite(
	ctx context.Context, s tenant.Scope, table string,
	componentID, bindingID uuid.UUID, kind string, contentHash string, headline collector.Headline,
	insertFn func(tx *sqlx.Tx) (uuid.UUID, error),
) (id uuid.UUID, inserted bool, err error) {
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		touchedID, touched, derr := dedupeOrTouch(ctx, tx, table, bindingID, contentHash)
		if derr != nil {
			return derr
		}
		if touched {
			id = touchedID
		} else {
			id, derr = insertFn(tx)
			if derr != nil {
				return derr
			}
			inserted = true
			if at, ok := observedAt(ctx); ok {
				// A historical write (WithObservedAt): the row was
				// observed at `at`, not now. captured_at is the partition
				// key, so Postgres moves the row to at's month.
				if _, derr = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET captured_at = $2, collected_at = $2 WHERE id = $1`, table), id, at); derr != nil {
					return derr
				}
			}
		}
		if _, historical := observedAt(ctx); historical {
			return nil // the dashboard's current state is live collection's
		}
		projectID, derr := componentProjectID(ctx, tx, componentID)
		if derr != nil {
			return derr
		}
		return upsertComponentMetricTx(ctx, tx, s.OrgID(), projectID, componentID, kind, headline)
	})
	return id, inserted, err
}

// dedupeOrTouch looks at table's most recently *collected* row for
// bindingID; if its content_hash equals contentHash, it bumps that row's
// collected_at and reports touched=true. captured_at is deliberately left
// alone: it's the partition key (task 3.6's monthly partitioning), and
// for sentry_snapshots specifically it's also what sentry_issues' FK
// pins to via a denormalized column — mutating it on a touch would
// either orphan that FK or require an ON UPDATE CASCADE that fans out to
// every issue row for no reason. collected_at is the freshness signal
// dedupe is actually about ("we re-confirmed this content, we didn't
// create a new one"); captured_at stays fixed at first-insert time. An
// empty contentHash never matches (a kind that doesn't participate in
// dedupe, or a caller that couldn't compute one, always gets a fresh
// row). table is always one of a small, fixed set of caller-supplied
// literal table names — never derived from request input — so building
// the query with fmt.Sprintf here is not an injection risk.
func dedupeOrTouch(ctx context.Context, tx *sqlx.Tx, table string, bindingID uuid.UUID, contentHash string) (touchedID uuid.UUID, touched bool, err error) {
	if contentHash == "" {
		return uuid.Nil, false, nil
	}
	var latestID uuid.UUID
	var latestHash sql.NullString
	q := fmt.Sprintf(`SELECT id, content_hash FROM %s WHERE binding_id = $1 ORDER BY collected_at DESC LIMIT 1`, table)
	args := []any{bindingID}
	at, historical := observedAt(ctx)
	if historical {
		// Only the history up to `at` is this observation's predecessor;
		// a later (live) row must never be touched with an old time.
		q = fmt.Sprintf(`SELECT id, content_hash FROM %s WHERE binding_id = $1 AND captured_at <= $2 ORDER BY captured_at DESC LIMIT 1`, table)
		args = append(args, at)
	}
	err = tx.QueryRowContext(ctx, q, args...).Scan(&latestID, &latestHash)
	if err != nil {
		if isNoRows(err) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	if !latestHash.Valid || latestHash.String != contentHash {
		return uuid.Nil, false, nil
	}
	touchQ := fmt.Sprintf(`UPDATE %s SET collected_at = now() WHERE id = $1`, table)
	touchArgs := []any{latestID}
	if historical {
		touchQ = fmt.Sprintf(`UPDATE %s SET collected_at = GREATEST(collected_at, $2) WHERE id = $1`, table)
		touchArgs = append(touchArgs, at)
	}
	if _, err := tx.ExecContext(ctx, touchQ, touchArgs...); err != nil {
		return uuid.Nil, false, err
	}
	return latestID, true, nil
}

// componentProjectID looks up a component's project_id — needed because
// component_metrics_current denormalizes it (so a project-scoped
// dashboard read never joins back to components), but PersistFunc
// implementations only ever receive componentID.
func componentProjectID(ctx context.Context, tx *sqlx.Tx, componentID uuid.UUID) (uuid.UUID, error) {
	var projectID uuid.UUID
	err := tx.GetContext(ctx, &projectID, `SELECT project_id FROM components WHERE id = $1`, componentID)
	return projectID, err
}
