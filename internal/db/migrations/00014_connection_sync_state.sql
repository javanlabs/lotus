-- +goose Up
-- Fixes a bug in migration 00013, caught before it shipped any working
-- code depending on it: the claim query needs rate_limited_until/
-- backoff_until across every organization's connections in one query
-- (same reasoning as sync_runs itself being unscoped), but those columns
-- were added to integration_connections — which IS subject to RLS (task
-- 0.3). With app.org_id unset (the claim query's whole point is running
-- unscoped, across all orgs), RLS hides every row of integration_connections
-- unconditionally, so a JOIN against it returns nothing at all, for any
-- organization — verified directly against Postgres before writing this
-- fix. RLS filters rows, not columns; there was no way to read even a
-- non-sensitive timestamp column off an RLS'd table without app.org_id
-- matching that row's own org.
--
-- connection_sync_state is a 1:1 sidecar to integration_connections,
-- holding only scheduler-internal operational state, explicitly NOT
-- RLS'd — same exception as sync_runs, for the same reason.
CREATE TABLE connection_sync_state (
    connection_id                  uuid PRIMARY KEY REFERENCES integration_connections (id) ON DELETE CASCADE,
    rate_limited_until              timestamptz,
    backoff_until                   timestamptz,
    consecutive_upstream_failures   integer NOT NULL DEFAULT 0
);
GRANT SELECT, INSERT, UPDATE, DELETE ON connection_sync_state TO lotus_app;

ALTER TABLE integration_connections
    DROP COLUMN IF EXISTS rate_limited_until,
    DROP COLUMN IF EXISTS backoff_until,
    DROP COLUMN IF EXISTS consecutive_upstream_failures;

-- +goose Down
ALTER TABLE integration_connections
    ADD COLUMN rate_limited_until           timestamptz,
    ADD COLUMN backoff_until                 timestamptz,
    ADD COLUMN consecutive_upstream_failures integer NOT NULL DEFAULT 0;
DROP TABLE IF EXISTS connection_sync_state;
