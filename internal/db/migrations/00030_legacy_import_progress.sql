-- +goose Up
-- How far `lotus import-legacy` has brought each binding's legacy history
-- (internal/legacylotus). Kept apart from the snapshot tables so that an
-- imported row and a live one are never told apart by guesswork: legacy
-- rows are imported up to imported_to, and never at or after a binding's
-- first live observation.
CREATE TABLE legacy_import_progress (
    binding_id  uuid PRIMARY KEY,
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    imported_to timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (binding_id, org_id) REFERENCES metric_bindings (id, org_id) ON DELETE CASCADE
);
ALTER TABLE legacy_import_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE legacy_import_progress FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON legacy_import_progress
    USING (org_id = NULLIF(current_setting('app.org_id', true), '')::uuid)
    WITH CHECK (org_id = NULLIF(current_setting('app.org_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON legacy_import_progress TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS legacy_import_progress;
