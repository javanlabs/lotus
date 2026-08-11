-- +goose Up
-- Task 3.7: metric_facts (raw, 7-day range), metric_facts_hourly (1-month
-- range), metric_facts_daily (1-year range), and metric_keys (the
-- catalog that lets the metric set grow/shrink per kind without a
-- migration).

-- metric_facts: one row per Fact per Collect. Partitioned by
-- collected_at exactly like the four snapshot tables (migration 00018)
-- — same reasoning: 14-day retention on a table this write-heavy
-- (±190k rows/day/org per the spec's own volume estimate) means aging
-- out has to be DROP PARTITION, not a row-by-row DELETE.
--
-- snapshot_id is a plain uuid, not a real FK: a Fact can come from any
-- one of four different kind-specific snapshot tables (gitlab_snapshots,
-- sonar_snapshots, trivy_snapshots, sentry_snapshots), so there is no
-- single table a constraint could reference. It's nullable by the
-- spec's own design ("fact boleh hidup tanpa detail") and v1 never
-- populates it — Facts are self-sufficient (key+value+unit+time) and
-- don't need the join; the column exists for a future reader that wants
-- to jump from a chart point to its detail row.
CREATE TABLE metric_facts (
    id bigint GENERATED ALWAYS AS IDENTITY,
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    snapshot_id uuid,
    key text NOT NULL,
    value_num double precision,
    value_text text,
    unit text NOT NULL DEFAULT '',
    collected_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, collected_at),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
) PARTITION BY RANGE (collected_at);
-- Column order is deliberate, not taste (the spec's own words): every
-- Series query filters by binding_id + key first, orders by
-- collected_at — this index serves that access path directly.
CREATE INDEX idx_metric_facts_binding_key_collected ON metric_facts (org_id, binding_id, key, collected_at DESC);

-- metric_facts_hourly / metric_facts_daily: rollup tiers. Not
-- partitioned — both are small relative to raw (a bucket per
-- binding+key per hour/day, not per collect) and short/medium-lived
-- (45 / 400 days), so a plain table with a time-range DELETE is enough;
-- partitioning every table "just in case" is exactly the premature
-- optimization task 3.6 already pushed back on for sync_runs.
CREATE TABLE metric_facts_hourly (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    key text NOT NULL,
    bucket timestamptz NOT NULL,
    value_min double precision,
    value_max double precision,
    value_avg double precision,
    value_last double precision,
    sample_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (binding_id, key, bucket),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_metric_facts_hourly_org ON metric_facts_hourly (org_id);

CREATE TABLE metric_facts_daily (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    key text NOT NULL,
    bucket timestamptz NOT NULL,
    value_min double precision,
    value_max double precision,
    value_avg double precision,
    value_last double precision,
    sample_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (binding_id, key, bucket),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_metric_facts_daily_org ON metric_facts_daily (org_id);

-- metric_keys: the catalog. declared comes from a kind's own
-- Descriptor.MetricKeys() (task 3.1); active freezes to false (never
-- deleted) once internal/timeseries's staleness sweep decides the key
-- stopped being emitted — see that package's own comment for the
-- detection window, since a single Collect has no way to know on its
-- own whether a key it didn't just emit is "gone" or just "not due yet".
CREATE TABLE metric_keys (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    kind text NOT NULL,
    key text NOT NULL,
    label text NOT NULL DEFAULT '',
    unit text NOT NULL DEFAULT '',
    value_type text NOT NULL DEFAULT 'number',
    direction text NOT NULL DEFAULT 'neutral' CHECK (direction IN ('higher_better', 'lower_better', 'neutral')),
    declared boolean NOT NULL DEFAULT false,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    active boolean NOT NULL DEFAULT true,
    PRIMARY KEY (org_id, kind, key)
);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
    m date;
BEGIN
    -- metric_facts partitions, same window as migration 00018's own
    -- initial set.
    FOR m IN SELECT generate_series(date_trunc('month', now()) - interval '1 month', date_trunc('month', now()) + interval '13 months', interval '1 month')::date
    LOOP
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF metric_facts FOR VALUES FROM (%L) TO (%L)',
            'metric_facts_' || to_char(m, 'YYYY_MM'), m, (m + interval '1 month')::date
        );
    END LOOP;

    FOREACH t IN ARRAY ARRAY['metric_facts', 'metric_facts_hourly', 'metric_facts_daily', 'metric_keys']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)',
            t
        );
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO lotus_app', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS metric_keys;
DROP TABLE IF EXISTS metric_facts_daily;
DROP TABLE IF EXISTS metric_facts_hourly;
DROP TABLE IF EXISTS metric_facts;
