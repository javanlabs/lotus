-- +goose Up
-- Task 3.3: gitlab_snapshots and sonar_snapshots, ported column-for-column
-- from the legacy Lotus's SQLite schema (migrations 001/002/011/013/014
-- at /Users/uyab/Code/lotus/web/internal/migrations), with the standard
-- tenant-plane preamble every task-0.3-pattern table gets: org_id,
-- binding_id, component_id, composite FK to metric_bindings, RLS FORCEd,
-- append-only (no UPDATE/DELETE path — a new collect just inserts another
-- row; task 3.6 handles retention/rollup, not this migration).
CREATE TABLE gitlab_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    primary_language text,
    languages_json jsonb,
    default_branch text,
    branches_total integer,
    branches_active integer,
    branches_stale integer,
    default_branch_protected boolean,
    visibility text,
    archived boolean,
    web_url text,
    last_activity_at timestamptz,
    commits_30d integer,
    commits_30d_capped boolean,
    last_committer_name text,
    last_pipeline_status text,
    last_pipeline_id integer,
    last_pipeline_duration_sec integer,
    last_pipeline_web_url text,
    last_pipeline_finished_at timestamptz,
    last_pipeline_ref text,
    pipeline_success_rate double precision,
    pipeline_sample_size integer,
    warnings_json jsonb,
    raw_json jsonb,
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_gitlab_snapshots_binding_captured ON gitlab_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_gitlab_snapshots_component_captured ON gitlab_snapshots (component_id, captured_at DESC);

CREATE TABLE sonar_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    quality_gate text,
    coverage double precision,
    bugs integer,
    vulnerabilities integer,
    code_smells integer,
    security_hotspots integer,
    duplicated_lines_density double precision,
    ncloc integer,
    complexity integer,
    reliability_rating text,
    security_rating text,
    sqale_rating text,
    sqale_index_min integer,
    test_execution_time_ms integer,
    languages text,
    last_scan_at timestamptz,
    raw_json jsonb,
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_sonar_snapshots_binding_captured ON sonar_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_sonar_snapshots_component_captured ON sonar_snapshots (component_id, captured_at DESC);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['gitlab_snapshots', 'sonar_snapshots']
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

GRANT SELECT, INSERT, UPDATE, DELETE ON gitlab_snapshots, sonar_snapshots TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS sonar_snapshots;
DROP TABLE IF EXISTS gitlab_snapshots;
