-- +goose Up
-- Task 3.4: trivy_snapshots + trivy_finding_notes, redesigned (not a
-- column-for-column port like task 3.3's tables) from the legacy Lotus's
-- SQLite schema (migrations 007/012/015/016/018 at
-- /Users/uyab/Code/lotus/web/internal/migrations). The old shape keyed
-- everything by image name, with service TEXT nullable ("best-effort
-- match to services.yaml") — the SaaS model inverts that: binding_id is
-- mandatory and IS the attribution, an image with no binding simply
-- isn't written here at all (it surfaces in discovered_resources,
-- task 2.4, until someone claims it).
CREATE TABLE trivy_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    component_id uuid NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    -- 'registry' (Harbor pull) vs 'push' (CI ingest, task 3.4) — renamed
    -- from the legacy 'harbor'/'manual' pair to match this task's own
    -- acceptance criterion wording ("dibedain cuma sama source").
    source text NOT NULL CHECK (source IN ('registry', 'push')),
    digest text,
    tags text[] NOT NULL DEFAULT '{}', -- was a comma-joined string; a real array now
    severity text,
    total integer NOT NULL DEFAULT 0,
    fixable integer NOT NULL DEFAULT 0,
    critical integer NOT NULL DEFAULT 0,
    high integer NOT NULL DEFAULT 0,
    medium integer NOT NULL DEFAULT 0,
    low integer NOT NULL DEFAULT 0,
    unknown integer NOT NULL DEFAULT 0,
    pulls integer,               -- registry-only; NULL for a push
    artifact_count integer,      -- registry-only; NULL for a push
    image_pushed_at timestamptz,
    scan_finished_at timestamptz,
    raw_json jsonb,
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_trivy_snapshots_binding_captured ON trivy_snapshots (binding_id, captured_at DESC);
CREATE INDEX idx_trivy_snapshots_component_captured ON trivy_snapshots (component_id, captured_at DESC);

-- Human false-positive annotations, re-keyed from (image, cve_id, package)
-- to (binding_id, cve_id, package) — task 3.4's own point: repointing a
-- component to a new image ref (a rename, a registry migration) must not
-- silently lose every annotation a team already made.
CREATE TABLE trivy_finding_notes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    binding_id uuid NOT NULL,
    cve_id text NOT NULL,
    package text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'false_positive',
    comment text NOT NULL,
    ai_generated boolean NOT NULL DEFAULT false,
    author_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (binding_id, cve_id, package),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id)
);
CREATE INDEX idx_trivy_finding_notes_binding ON trivy_finding_notes (binding_id);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['trivy_snapshots', 'trivy_finding_notes']
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

GRANT SELECT, INSERT, UPDATE, DELETE ON trivy_snapshots, trivy_finding_notes TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS trivy_finding_notes;
DROP TABLE IF EXISTS trivy_snapshots;
