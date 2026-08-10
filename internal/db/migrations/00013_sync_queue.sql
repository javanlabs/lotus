-- +goose Up
-- Task 3.2: the sync queue. Replaces per-integration time.Ticker
-- goroutines with a database-backed job queue — dedupe, crash recovery,
-- horizontal scale, and an activity feed all come from one extra table
-- instead of hand-rolled coordination per collector.

ALTER TABLE integration_connections
    ADD COLUMN rate_limited_until          timestamptz,
    ADD COLUMN backoff_until                timestamptz,
    ADD COLUMN consecutive_upstream_failures integer NOT NULL DEFAULT 0;

-- Concurrency quota per organization (task 3.2's fairness mechanism #1):
-- caps how many runs across an entire org can be leased/running at once,
-- so one organization with 900 components can't starve every other
-- organization's queue. Schema-ready now, hardcoded default for v1 (no
-- per-org override UI yet) — same reasoning as organizations.retention_days
-- in task 3.6: this is cheap to add now and expensive to retrofit onto a
-- populated table later.
ALTER TABLE organizations
    ADD COLUMN sync_concurrency_quota integer NOT NULL DEFAULT 4;

-- sync_runs deliberately carries org_id but is NOT subject to RLS, unlike
-- every other table with that column in this schema. The scheduler's
-- claim query is inherently cross-tenant — it fairly picks the next
-- runnable job across every organization at once (task 3.2's whole
-- fairness mechanism depends on seeing all of them in one query) — so
-- scoping it to a single app.org_id, the way tenant.Scope does for every
-- request-driven query, isn't just inconvenient here, it's incompatible
-- with the table's actual access pattern. It's accessed exclusively
-- through internal/repo/global (like the control-plane tables), with
-- explicit "WHERE org_id = $1" filtering applied in application code
-- wherever a single organization's own view (a future Activity page,
-- task 4.5) is what's needed.
CREATE TABLE sync_runs (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id            uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    connection_id     uuid NOT NULL,
    binding_id        uuid NOT NULL,
    kind              text NOT NULL,
    trigger           text NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    priority          integer NOT NULL DEFAULT 100,
    scheduled_for     timestamptz NOT NULL DEFAULT now(),
    leased_at         timestamptz,
    lease_expires_at  timestamptz,
    worker_id         text,
    status            text NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'leased', 'running', 'ok', 'partial', 'failed', 'skipped')),
    attempt           integer NOT NULL DEFAULT 0,
    error_class       text,
    error             text,
    warnings_json     jsonb,
    metrics_json      jsonb,
    started_at        timestamptz,
    finished_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (connection_id, org_id) REFERENCES integration_connections (id, org_id),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id)
);

-- The entire dedupe mechanism (task 3.2): at most one queued/leased/
-- running run per binding at a time. A manual trigger is the identical
-- INSERT a scheduled trigger would issue, just trigger='manual' — when
-- one's already in flight, the insert's ON CONFLICT (below, in the Go
-- enqueue path) makes it a no-op instead of a second, competing run.
CREATE UNIQUE INDEX uq_run_inflight ON sync_runs (binding_id)
    WHERE status IN ('queued', 'leased', 'running');

-- The claim query's own access path: queued runs ordered by priority
-- then scheduled_for.
CREATE INDEX idx_sync_runs_claim ON sync_runs (priority, scheduled_for)
    WHERE status = 'queued';

-- Reaper's access path: leased/running runs whose lease has expired.
CREATE INDEX idx_sync_runs_lease_expiry ON sync_runs (lease_expires_at)
    WHERE status IN ('leased', 'running');

-- Activity feed / retention job access path (task 3.6).
CREATE INDEX idx_sync_runs_org_created ON sync_runs (org_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON sync_runs TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS sync_runs;
ALTER TABLE organizations DROP COLUMN IF EXISTS sync_concurrency_quota;
ALTER TABLE integration_connections
    DROP COLUMN IF EXISTS rate_limited_until,
    DROP COLUMN IF EXISTS backoff_until,
    DROP COLUMN IF EXISTS consecutive_upstream_failures;
