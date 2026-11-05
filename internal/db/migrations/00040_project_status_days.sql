-- +goose Up
-- The public status page (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §6)
-- reports a project's verdict for a chosen day. component_metrics_current
-- only knows *now*, and the snapshot tables keep raw values but not the
-- status each sync produced under the binding settings of that time, so a
-- day's verdict is recorded here as it happens: one row per project per
-- WIB day, rewritten for today whenever the dashboard projection changes,
-- never touched once the day is over.
--
-- components is the list of the project's live components that day, each
-- with its display-safe kind headlines (no summaries, no links). There is
-- deliberately no foreign key to components: history outlives a deleted
-- component, so a red day sent to a client stays red when reopened.
CREATE TABLE project_status_days (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    day         date NOT NULL,
    status      text NOT NULL CHECK (status IN ('pass', 'fail', 'unknown')),
    score       integer,
    components  jsonb NOT NULL DEFAULT '[]'::jsonb,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, day),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_project_status_days_day ON project_status_days (day);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE project_status_days ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE project_status_days FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON project_status_days USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON project_status_days TO lotus_app;

-- The publish switch: NULL is off, the default. Only an owner or admin
-- sets it (project.status_page).
ALTER TABLE projects ADD COLUMN status_page_enabled_at timestamptz;

-- The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
JOIN (VALUES ('owner', 'project.status_page'), ('admin', 'project.status_page')) AS p (role_key, permission) ON p.role_key = r.key
WHERE r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission = 'project.status_page';
ALTER TABLE projects DROP COLUMN IF EXISTS status_page_enabled_at;
DROP TABLE IF EXISTS project_status_days;
