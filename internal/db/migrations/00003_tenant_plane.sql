-- +goose Up
-- Tenant plane: everything owned by a single organization. Every table
-- carries org_id NOT NULL (never reached via join, even for leaf tables),
-- Row-Level Security is FORCEd on, and every parent/child pair is linked by
-- a composite foreign key (child_fk_id, org_id) REFERENCES parent(id,
-- org_id) so a child row's org_id can't drift from its parent's — Postgres
-- rejects the insert outright instead of relying on application code to
-- keep them in sync.
--
-- Two Postgres roles enforce the DDL/DML split: lotus_migrate (this
-- migration's own connecting role in production, via MIGRATE_DATABASE_URL)
-- owns every table; lotus_app (the app's runtime role) gets only
-- SELECT/INSERT/UPDATE/DELETE, so a bug in application code can mutate
-- tenant data but can never touch schema. Created here (idempotently, race
-- -safe against concurrent migration runs across parallel test schemas)
-- rather than in a deploy script, so the same guarantee holds in every test
-- database, not only production.
-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        CREATE ROLE lotus_migrate NOLOGIN;
    EXCEPTION WHEN duplicate_object THEN NULL;
    END;
    BEGIN
        CREATE ROLE lotus_app NOLOGIN;
    EXCEPTION WHEN duplicate_object THEN NULL;
    END;
END
$$;
-- +goose StatementEnd

CREATE TABLE projects (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    slug         text NOT NULL,
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    archived_at  timestamptz,
    sort_order   integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slug),
    UNIQUE (id, org_id) -- composite-FK anchor for components
);

CREATE TABLE components (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id   uuid NOT NULL,
    slug         text NOT NULL,
    name         text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('backend', 'frontend', 'mobile', 'service', 'library')),
    language     text NOT NULL DEFAULT '',
    legacy_name  text, -- task 5.1 import only
    archived_at  timestamptz,
    sort_order   integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug),
    UNIQUE (id, org_id), -- composite-FK anchor for metric_bindings
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id)
);

CREATE TABLE integration_connections (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    provider            text NOT NULL CHECK (provider IN ('gitlab', 'sonarqube', 'harbor_trivy', 'sentry')),
    name                text NOT NULL,
    base_url            text NOT NULL,
    config              jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_ciphertext   bytea,
    secret_nonce        bytea,
    secret_key_version  integer,
    secret_fingerprint  text,
    status              text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'auth_failed', 'error')),
    last_verified_at    timestamptz,
    last_error          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, provider, name),
    UNIQUE (id, org_id) -- composite-FK anchor for metric_bindings / discovered_resources
);

CREATE TABLE metric_bindings (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                 uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    component_id           uuid NOT NULL,
    connection_id          uuid NOT NULL,
    kind                   text NOT NULL,
    external_key           text NOT NULL,
    config                 jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled                boolean NOT NULL DEFAULT true,
    sync_interval_seconds  integer,
    next_sync_at           timestamptz,
    last_status            text,
    consecutive_failures   integer NOT NULL DEFAULT 0,
    deleted_at             timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id), -- composite-FK anchor for discovered_resources.bound_binding_id
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id),
    FOREIGN KEY (connection_id, org_id) REFERENCES integration_connections (id, org_id)
);
-- One component can have two bindings of the same kind (e.g. two GitLab
-- repos), so the unique key is the full triple, not (component_id, kind).
CREATE UNIQUE INDEX uq_metric_bindings_target ON metric_bindings (component_id, kind, external_key)
    WHERE deleted_at IS NULL;
-- Scheduler claim query index (task 3.2): only enabled, non-deleted, due bindings.
CREATE INDEX idx_metric_bindings_claim ON metric_bindings (next_sync_at)
    WHERE enabled AND deleted_at IS NULL;

CREATE TABLE discovered_resources (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    connection_id     uuid NOT NULL,
    kind              text NOT NULL,
    external_key      text NOT NULL,
    label             text NOT NULL DEFAULT '',
    metadata          jsonb NOT NULL DEFAULT '{}'::jsonb,
    bound_binding_id  uuid,
    ignored_at        timestamptz,
    first_seen_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (connection_id, kind, external_key),
    FOREIGN KEY (connection_id, org_id) REFERENCES integration_connections (id, org_id),
    FOREIGN KEY (bound_binding_id, org_id) REFERENCES metric_bindings (id, org_id)
);

-- Row-Level Security: FORCE so even the table owner (lotus_migrate) is
-- subject to it — without FORCE, ownership alone bypasses policy. One
-- policy per table, identical shape, covering every command: a single
-- equality on org_id, so the planner folds it into an index scan instead of
-- evaluating a per-row EXISTS subquery. current_setting(..., true) (missing
-- -ok) returns NULL rather than erroring when app.org_id was never set, and
-- "org_id = NULL" is never true — so a forgotten SET LOCAL yields zero
-- rows, not an error and not every org's rows.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['projects', 'components', 'integration_connections', 'metric_bindings', 'discovered_resources']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = current_setting(''app.org_id'', true)::uuid) WITH CHECK (org_id = current_setting(''app.org_id'', true)::uuid)',
            t
        );
    END LOOP;
END
$$;
-- +goose StatementEnd

-- lotus_app gets DML only, never DDL, on every table that exists so far
-- (control plane from migration 00002, plus tenant plane above) — and, via
-- ALTER DEFAULT PRIVILEGES, on every table any future migration adds under
-- the same migrating role, so this grant doesn't have to be repeated by
-- hand in every later migration.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO lotus_app', current_schema());
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO lotus_app', current_schema());
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO lotus_app',
        current_user, current_schema()
    );
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS discovered_resources;
DROP TABLE IF EXISTS metric_bindings;
DROP TABLE IF EXISTS integration_connections;
DROP TABLE IF EXISTS components;
DROP TABLE IF EXISTS projects;
