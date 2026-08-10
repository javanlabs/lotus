package global

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// SyncRun is one row of task 3.2's job queue. Deliberately lives in
// internal/repo/global, not internal/repo: sync_runs is NOT subject to
// RLS (see migration 00013's comment on the table) because the claim
// query is inherently cross-tenant, exactly the shape internal/repo/global
// already exists for.
type SyncRun struct {
	ID             int64      `db:"id"`
	OrgID          uuid.UUID  `db:"org_id"`
	ConnectionID   uuid.UUID  `db:"connection_id"`
	BindingID      uuid.UUID  `db:"binding_id"`
	Kind           string     `db:"kind"`
	Trigger        string     `db:"trigger"`
	Priority       int        `db:"priority"`
	ScheduledFor   time.Time  `db:"scheduled_for"`
	LeasedAt       *time.Time `db:"leased_at"`
	LeaseExpiresAt *time.Time `db:"lease_expires_at"`
	WorkerID       *string    `db:"worker_id"`
	Status         string     `db:"status"`
	Attempt        int        `db:"attempt"`
	ErrorClass     *string    `db:"error_class"`
	Error          *string    `db:"error"`
	// []byte, not json.RawMessage: these two columns are nullable (only
	// populated once a run finishes), and database/sql's NULL-scanning
	// special case is for *[]byte specifically — a named []byte type like
	// json.RawMessage doesn't hit it and fails to scan a NULL. Callers
	// that want json.RawMessage can convert for free (RawMessage is
	// defined as []byte).
	WarningsJSON []byte     `db:"warnings_json"`
	MetricsJSON  []byte     `db:"metrics_json"`
	StartedAt    *time.Time `db:"started_at"`
	FinishedAt   *time.Time `db:"finished_at"`
	CreatedAt    time.Time  `db:"created_at"`
}

const syncRunColumns = `id, org_id, connection_id, binding_id, kind, trigger, priority, scheduled_for,
	leased_at, lease_expires_at, worker_id, status, attempt, error_class, error,
	warnings_json, metrics_json, started_at, finished_at, created_at`

type NewSyncRun struct {
	OrgID        uuid.UUID
	ConnectionID uuid.UUID
	BindingID    uuid.UUID
	Kind         string
	Trigger      string // "scheduled" | "manual"
	Priority     int
	ScheduledFor time.Time
}

// EnqueueRun inserts a run, or does nothing if one is already in flight
// for this binding (uq_run_inflight) — a manual trigger racing a
// scheduled one, or two scheduler ticks racing each other, both resolve
// to exactly one run rather than two competing ones. Returns the run id
// and whether this call actually inserted it (false means it deduped
// against an existing in-flight run).
func EnqueueRun(ctx context.Context, db *sqlx.DB, in NewSyncRun) (id int64, inserted bool, err error) {
	if in.Priority == 0 {
		in.Priority = 100
	}
	err = db.GetContext(ctx, &id, `
		INSERT INTO sync_runs (org_id, connection_id, binding_id, kind, trigger, priority, scheduled_for)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (binding_id) WHERE status IN ('queued', 'leased', 'running') DO NOTHING
		RETURNING id`,
		in.OrgID, in.ConnectionID, in.BindingID, in.Kind, in.Trigger, in.Priority, in.ScheduledFor)
	if err != nil {
		if isNoRowsGlobal(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}

// ClaimRun implements task 3.2's claim query: the next queued run whose
// connection isn't currently rate-limited or backing off, skipping any
// organization already at its concurrency quota, locked with FOR UPDATE
// SKIP LOCKED so concurrent workers never claim the same row. Returns nil
// (no error) when nothing is claimable right now.
//
// The join here is against connection_sync_state (migration 00014), not
// integration_connections directly — that table is RLS'd (task 0.3), and
// with app.org_id necessarily unset for a query that claims fairly across
// every organization at once, RLS hides every one of its rows
// unconditionally, for any org. connection_sync_state is a deliberately
// unscoped sidecar holding only this operational state, the same
// exception sync_runs itself is. A connection with no sidecar row yet
// (LEFT JOIN) is treated as never rate-limited/backing-off, the correct
// default.
func ClaimRun(ctx context.Context, db *sqlx.DB, workerID string, excludedOrgIDs []uuid.UUID, leaseDuration time.Duration) (*SyncRun, error) {
	if excludedOrgIDs == nil {
		excludedOrgIDs = []uuid.UUID{}
	}
	var run SyncRun
	err := db.GetContext(ctx, &run, `
		UPDATE sync_runs SET status = 'leased', leased_at = now(),
		       lease_expires_at = now() + ($3::text || ' seconds')::interval, worker_id = $1
		WHERE id = (
			SELECT r.id FROM sync_runs r
			LEFT JOIN connection_sync_state cs ON cs.connection_id = r.connection_id
			WHERE r.status = 'queued'
			  AND (cs.rate_limited_until IS NULL OR cs.rate_limited_until < now())
			  AND (cs.backoff_until      IS NULL OR cs.backoff_until      < now())
			  AND r.org_id <> ALL($2::uuid[])
			ORDER BY r.priority, r.scheduled_for
			FOR UPDATE OF r SKIP LOCKED LIMIT 1
		)
		RETURNING `+syncRunColumns,
		workerID, pqUUIDArray(excludedOrgIDs), strconv.Itoa(int(leaseDuration.Seconds())))
	if err != nil {
		if isNoRowsGlobal(err) {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

// MarkRunStarted transitions a leased run to running, right before the
// worker actually calls the collector — kept distinct from the claim
// itself so a run's status can tell "claimed but not yet dispatched" (a
// worker queue backlog) apart from "actively calling upstream right now".
func MarkRunStarted(ctx context.Context, db *sqlx.DB, id int64) error {
	_, err := db.ExecContext(ctx, `UPDATE sync_runs SET status = 'running', started_at = now() WHERE id = $1`, id)
	return err
}

type FinishRunParams struct {
	Status       string // "ok" | "partial" | "failed" | "skipped"
	ErrorClass   *string
	Error        *string
	WarningsJSON json.RawMessage
	MetricsJSON  json.RawMessage
}

func FinishRun(ctx context.Context, db *sqlx.DB, id int64, p FinishRunParams) error {
	_, err := db.ExecContext(ctx, `
		UPDATE sync_runs
		SET status = $2, error_class = $3, error = $4, warnings_json = $5, metrics_json = $6,
		    finished_at = now(), leased_at = NULL, lease_expires_at = NULL, worker_id = NULL
		WHERE id = $1`,
		id, p.Status, p.ErrorClass, p.Error, nullIfEmptyJSON(p.WarningsJSON), nullIfEmptyJSON(p.MetricsJSON))
	return err
}

// ReapExpiredLeases returns any leased/running run whose lease has
// expired back to queued, incrementing its attempt — task 3.2's answer to
// "the process died mid-sync", which the old codebase had no answer for
// at all (a job_runs row would sit at "running" forever).
func ReapExpiredLeases(ctx context.Context, db *sqlx.DB) (int, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE sync_runs
		SET status = 'queued', attempt = attempt + 1, leased_at = NULL, lease_expires_at = NULL, worker_id = NULL
		WHERE status IN ('leased', 'running') AND lease_expires_at < now()`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// OrgsAtCapacity returns every org_id currently holding leased/running
// runs at or above its own sync_concurrency_quota — task 3.2's fairness
// mechanism #1, fed straight into ClaimRun's exclusion list.
func OrgsAtCapacity(ctx context.Context, db *sqlx.DB) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := db.SelectContext(ctx, &ids, `
		SELECT r.org_id
		FROM sync_runs r
		JOIN organizations o ON o.id = r.org_id
		WHERE r.status IN ('leased', 'running')
		GROUP BY r.org_id, o.sync_concurrency_quota
		HAVING count(*) >= o.sync_concurrency_quota`)
	return ids, err
}

func isNoRowsGlobal(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func nullIfEmptyJSON(b json.RawMessage) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// pqUUIDArray formats a []uuid.UUID for Postgres's ANY()/<>ALL() array
// syntax without pulling in pq.Array here (this package already depends
// on pq.StringArray elsewhere for a different reason; a plain string
// build keeps this one function dependency-free).
func pqUUIDArray(ids []uuid.UUID) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}
