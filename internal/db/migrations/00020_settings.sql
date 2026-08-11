-- +goose Up
-- Task 4.5: schema support for the settings screens.
--
-- organizations.timezone / default_sync_interval_seconds — surfaced on
-- /settings/organization. default_sync_interval_seconds is nullable: null
-- means "use the built-in scheduler.DefaultSyncInterval", the same
-- fallback every binding already had before this org-level knob existed.
ALTER TABLE organizations ADD COLUMN timezone text NOT NULL DEFAULT 'UTC';
ALTER TABLE organizations ADD COLUMN default_sync_interval_seconds integer;

-- integration_connections gets the same soft-delete shape metric_bindings
-- already has, so /settings/connections' delete action can cascade to
-- soft-deleting every binding on the connection in one transaction
-- without ever hitting metric_bindings' composite FK (which is RESTRICT,
-- not CASCADE, by design — task 0.3's anti-drift pattern doesn't want a
-- silent hard-delete fan-out).
ALTER TABLE integration_connections ADD COLUMN deleted_at timestamptz;

-- The old table-level UNIQUE (org_id, provider, name) would otherwise
-- block re-creating a connection under a name whose previous holder was
-- soft-deleted — replaced with the same "only live rows" partial index
-- shape metric_bindings' own uq_metric_bindings_target already uses.
ALTER TABLE integration_connections DROP CONSTRAINT integration_connections_org_id_provider_name_key;
CREATE UNIQUE INDEX uq_integration_connections_target ON integration_connections (org_id, provider, name)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_integration_connections_target;
ALTER TABLE integration_connections ADD CONSTRAINT integration_connections_org_id_provider_name_key UNIQUE (org_id, provider, name);
ALTER TABLE integration_connections DROP COLUMN deleted_at;
ALTER TABLE organizations DROP COLUMN default_sync_interval_seconds;
ALTER TABLE organizations DROP COLUMN timezone;
