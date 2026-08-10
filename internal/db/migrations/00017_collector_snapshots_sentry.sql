-- +goose Up
-- Task 3.5: sentry_snapshots + sentry_issues, redesigned from the legacy
-- Lotus's SQLite schema (migrations 020_sentry.sql, 022_sentry_service_tag.sql
-- at /Users/uyab/Code/lotus/web/internal/migrations) the same way task 3.4's
-- trivy_snapshots was: the legacy unique key was owned by the upstream
-- provider's own identity (project_slug, environment, service_name), which
-- two organizations can legitimately collide on (both can have a Sentry
-- project literally named "alurkerja-javan-be"). The SaaS key leads with
-- binding_id instead — that's the multi-tenancy fix task 3.5 calls out by
-- name.
CREATE TABLE sentry_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    -- environment is "all" for a binding with no explicit environments
    -- configured (task 3.5's opt-in-only environment fan-out: Collect never
    -- auto-discovers a project's environment list, only ever queries the
    -- ones the binding names). service_name is the binding's own
    -- service_tag_value (mode=shared) or "" (mode=dedicated) — kept in the
    -- unique key for parity with the legacy shape even though binding_id
    -- alone already disambiguates it.
    environment text NOT NULL,
    service_name text NOT NULL DEFAULT '',
    sentry_project_id bigint NOT NULL DEFAULT 0,
    project_slug text NOT NULL,
    project_name text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT '',
    sentry_org text NOT NULL DEFAULT '', -- Sentry's own org slug; unrelated to this table's org_id
    team_id bigint NOT NULL DEFAULT 0,
    team_slug text NOT NULL DEFAULT '',
    status_filter text NOT NULL DEFAULT '', -- the Sentry search query actually sent
    generated_at timestamptz,
    total_issues integer NOT NULL DEFAULT 0,
    -- Health score (internal/scoring.EvalSentryStatus), stored so the
    -- dashboard (task 3.6) doesn't recompute it from sentry_issues on every
    -- read. score/grade are NULL for the three "nothing to measure" states
    -- (not_mapped/no_data/no_signal); load is the raw pre-exponential
    -- burden, kept for the tooltip.
    score integer,
    grade text,
    load double precision,
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id),
    UNIQUE (binding_id, environment, service_name),
    UNIQUE (id, org_id) -- composite-FK anchor for sentry_issues
);
CREATE INDEX idx_sentry_snapshots_binding_captured ON sentry_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_sentry_snapshots_component_captured ON sentry_snapshots (component_id, captured_at DESC);

CREATE TABLE sentry_issues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    snapshot_id uuid NOT NULL,
    issue_id text NOT NULL,
    short_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    substatus text NOT NULL DEFAULT '',
    event_count integer NOT NULL DEFAULT 0,
    first_seen timestamptz,
    last_seen timestamptz,
    permalink text NOT NULL DEFAULT '',
    FOREIGN KEY (snapshot_id, org_id) REFERENCES sentry_snapshots (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_sentry_issues_snapshot ON sentry_issues (snapshot_id);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['sentry_snapshots', 'sentry_issues']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)',
            t
        );
    END LOOP;
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON sentry_snapshots, sentry_issues TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS sentry_issues;
DROP TABLE IF EXISTS sentry_snapshots;
