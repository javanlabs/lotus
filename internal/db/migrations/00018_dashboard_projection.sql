-- +goose Up
-- Task 3.6: the dashboard-current projection, write-dedupe support, and
-- monthly partitioning + retention for the four snapshot tables.

-- organizations.retention_days: schema-ready now even though the v1
-- retention job (see internal/retention) still uses one hardcoded global
-- window for its partition-drop decision — a per-org window would mean
-- per-org partitioning (or a row-level delete instead of DROP PARTITION,
-- losing the whole point), which is a bigger redesign than v1 needs.
-- Landing the column now means that redesign is a backfill, not a
-- migration on top of the biggest tables in the schema.
ALTER TABLE organizations ADD COLUMN retention_days integer NOT NULL DEFAULT 90;

-- component_metrics_current: one row per component, upserted by every
-- Persist alongside its detail write. metrics is keyed by kind name
-- ("gitlab", "sonarqube", "trivy", "sentry", ...) rather than one column
-- per kind, so a fifth integration never needs a migration here — see
-- internal/repo/component_metrics.go for the shape stored under each key
-- (collector.Headline, unchanged). status/score are the rollup across
-- every kind's Headline.Status/Score currently in metrics, recomputed by
-- the same upsert so a dashboard read never has to.
CREATE TABLE component_metrics_current (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id uuid NOT NULL,
    component_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'unknown' CHECK (status IN ('pass', 'warn', 'fail', 'unknown')),
    score integer,
    metrics jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (component_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id)
);
-- The dashboard's own read pattern (task 3.6's acceptance criterion: 50
-- components x 4 providers in one query) is "every component in this
-- project/org" — both already covered by an index-only scan off org_id
-- (RLS's own predicate) or this project_id index; neither needs a join
-- back to components.
CREATE INDEX idx_component_metrics_current_project ON component_metrics_current (project_id);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE component_metrics_current ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE component_metrics_current FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON component_metrics_current USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON component_metrics_current TO lotus_app;

-- ── Partition gitlab_snapshots, sonar_snapshots, trivy_snapshots ──
-- Postgres requires a partitioned table's PRIMARY KEY to include the
-- partition column, so `id` alone can no longer be the whole key; nothing
-- else references these three tables by id (no child table, unlike
-- sentry_snapshots below), so this is otherwise a mechanical recreate.
-- Aging out becomes DROP PARTITION (a catalog-only operation) instead of
-- a bulk DELETE + the vacuum work that follows one on a table this size —
-- the whole reason this migration exists says it plainly: the difference
-- between a 3-second maintenance window and a vacuum incident.
--
-- Empty tables (pre-launch, no real snapshot data written outside tests
-- yet) — recreated in place rather than migrated row-by-row.
ALTER TABLE gitlab_snapshots RENAME TO gitlab_snapshots_old;
CREATE TABLE gitlab_snapshots (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    content_hash text,
    collected_at timestamptz NOT NULL DEFAULT now(),
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
    PRIMARY KEY (id, captured_at),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
) PARTITION BY RANGE (captured_at);
DROP TABLE gitlab_snapshots_old;
CREATE INDEX idx_gitlab_snapshots_binding_captured ON gitlab_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_gitlab_snapshots_binding_collected ON gitlab_snapshots (binding_id, collected_at DESC);
CREATE INDEX idx_gitlab_snapshots_component_captured ON gitlab_snapshots (component_id, captured_at DESC);

ALTER TABLE sonar_snapshots RENAME TO sonar_snapshots_old;
CREATE TABLE sonar_snapshots (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    content_hash text,
    collected_at timestamptz NOT NULL DEFAULT now(),
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
    PRIMARY KEY (id, captured_at),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
) PARTITION BY RANGE (captured_at);
DROP TABLE sonar_snapshots_old;
CREATE INDEX idx_sonar_snapshots_binding_captured ON sonar_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_sonar_snapshots_binding_collected ON sonar_snapshots (binding_id, collected_at DESC);
CREATE INDEX idx_sonar_snapshots_component_captured ON sonar_snapshots (component_id, captured_at DESC);

ALTER TABLE trivy_snapshots RENAME TO trivy_snapshots_old;
CREATE TABLE trivy_snapshots (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    content_hash text,
    collected_at timestamptz NOT NULL DEFAULT now(),
    source text NOT NULL CHECK (source IN ('registry', 'push')),
    digest text,
    tags text[] NOT NULL DEFAULT '{}',
    severity text,
    total integer NOT NULL DEFAULT 0,
    fixable integer NOT NULL DEFAULT 0,
    critical integer NOT NULL DEFAULT 0,
    high integer NOT NULL DEFAULT 0,
    medium integer NOT NULL DEFAULT 0,
    low integer NOT NULL DEFAULT 0,
    unknown integer NOT NULL DEFAULT 0,
    pulls integer,
    artifact_count integer,
    image_pushed_at timestamptz,
    scan_finished_at timestamptz,
    raw_json jsonb,
    PRIMARY KEY (id, captured_at),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
) PARTITION BY RANGE (captured_at);
DROP TABLE trivy_snapshots_old;
CREATE INDEX idx_trivy_snapshots_binding_captured ON trivy_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_trivy_snapshots_binding_collected ON trivy_snapshots (binding_id, collected_at DESC);
CREATE INDEX idx_trivy_snapshots_component_captured ON trivy_snapshots (component_id, captured_at DESC);

-- ── Partition sentry_snapshots (has a child table: sentry_issues) ──
-- sentry_issues does NOT carry a real FK to sentry_snapshots, unlike
-- every other parent/child pair in this schema — deliberately.
-- PostgreSQL's own partitioning implementation makes a FK from another
-- table to a partitioned parent a structural dependency of *every one of
-- its partitions*, not just the ones a row actually references: DROP
-- TABLE on any partition then fails ("other objects depend on it") even
-- when zero sentry_issues rows point at it, defeating the entire reason
-- these tables are partitioned in the first place (DROP PARTITION as a
-- catalog-only retention operation). snapshot_id/snapshot_captured_at
-- are kept as plain, indexed columns instead; consistency is maintained
-- at the application level — internal/repo.ReplaceSentrySnapshots writes
-- a snapshot and its issues in the same transaction and deletes a
-- snapshot's issues explicitly before deleting (or replacing) it, and
-- internal/retention.DropOldPartitions deletes a partition's
-- sentry_issues rows itself, by snapshot_captured_at, before dropping
-- the sentry_snapshots partition underneath them.
DROP TABLE sentry_issues;
ALTER TABLE sentry_snapshots RENAME TO sentry_snapshots_old;

CREATE TABLE sentry_snapshots (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    content_hash text,
    collected_at timestamptz NOT NULL DEFAULT now(),
    environment text NOT NULL,
    service_name text NOT NULL DEFAULT '',
    sentry_project_id bigint NOT NULL DEFAULT 0,
    project_slug text NOT NULL,
    project_name text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT '',
    sentry_org text NOT NULL DEFAULT '',
    team_id bigint NOT NULL DEFAULT 0,
    team_slug text NOT NULL DEFAULT '',
    status_filter text NOT NULL DEFAULT '',
    generated_at timestamptz,
    total_issues integer NOT NULL DEFAULT 0,
    score integer,
    grade text,
    load double precision,
    PRIMARY KEY (id, captured_at),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id),
    UNIQUE (binding_id, environment, service_name, captured_at)
) PARTITION BY RANGE (captured_at);
DROP TABLE sentry_snapshots_old;
CREATE INDEX idx_sentry_snapshots_binding_captured ON sentry_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_sentry_snapshots_binding_collected ON sentry_snapshots (binding_id, collected_at DESC);
CREATE INDEX idx_sentry_snapshots_component_captured ON sentry_snapshots (component_id, captured_at DESC);

CREATE TABLE sentry_issues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    snapshot_id uuid NOT NULL,
    snapshot_captured_at timestamptz NOT NULL,
    issue_id text NOT NULL,
    short_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    substatus text NOT NULL DEFAULT '',
    event_count integer NOT NULL DEFAULT 0,
    first_seen timestamptz,
    last_seen timestamptz,
    permalink text NOT NULL DEFAULT ''
    -- No FK to sentry_snapshots — see the CREATE TABLE sentry_snapshots
    -- comment above for why not: partitioning makes it impossible to
    -- ever drop an old partition once one exists.
);
CREATE INDEX idx_sentry_issues_snapshot ON sentry_issues (snapshot_id);
CREATE INDEX idx_sentry_issues_captured ON sentry_issues (snapshot_captured_at);

-- ── Initial partitions ──
-- One per month, this month plus a year-ish of headroom; the retention
-- loop (internal/retention) creates further months as it runs so this
-- migration never needs to be the thing that keeps up.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
    m date;
BEGIN
    FOREACH t IN ARRAY ARRAY['gitlab_snapshots', 'sonar_snapshots', 'trivy_snapshots', 'sentry_snapshots']
    LOOP
        FOR m IN SELECT generate_series(date_trunc('month', now()) - interval '1 month', date_trunc('month', now()) + interval '13 months', interval '1 month')::date
        LOOP
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
                t || '_' || to_char(m, 'YYYY_MM'), t, m, (m + interval '1 month')::date
            );
        END LOOP;
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)',
            t
        );
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO lotus_app', t);
    END LOOP;

    EXECUTE 'ALTER TABLE sentry_issues ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE sentry_issues FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON sentry_issues USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON sentry_issues TO lotus_app';
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS sentry_issues;
DROP TABLE IF EXISTS sentry_snapshots;
DROP TABLE IF EXISTS trivy_snapshots;
DROP TABLE IF EXISTS sonar_snapshots;
DROP TABLE IF EXISTS gitlab_snapshots;
DROP TABLE IF EXISTS component_metrics_current;
ALTER TABLE organizations DROP COLUMN retention_days;
