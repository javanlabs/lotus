-- +goose Up
-- Folders arrange an organization's projects into one shared tree
-- (docs/superpowers/specs/2026-09-26-project-folders-design.md). They are
-- arrangement only: members, connections and settings stay org-wide.
--
-- Both foreign keys below are the default NO ACTION rather than RESTRICT.
-- The two refuse a non-empty folder's deletion identically, but RESTRICT is
-- checked mid-statement, so deleting an organization — whose cascade
-- removes a parent folder while its children are still present — would
-- fail. NO ACTION is checked at the end of the statement, after the whole
-- cascade has run.
--
-- The composite (id, org_id) keys follow the components pattern: a folder
-- can only have a parent, and a project can only be filed in a folder,
-- from its own organization, even through a bug that forgot a scope.
CREATE TABLE folders (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    parent_id   uuid,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 80),
    sort_order  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (parent_id, org_id) REFERENCES folders (id, org_id)
);

-- Sibling names are unique ignoring case, and NULLS NOT DISTINCT makes the
-- top level (parent_id IS NULL) one sibling group too. Postgres 15+.
CREATE UNIQUE INDEX folders_sibling_name
    ON folders (org_id, parent_id, lower(name)) NULLS NOT DISTINCT;

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE folders ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE folders FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON folders USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON folders TO lotus_app;

-- NULL = the project sits at the root, which is where every existing
-- project stays until someone files it.
ALTER TABLE projects ADD COLUMN folder_id uuid;
ALTER TABLE projects ADD CONSTRAINT projects_folder_id_fkey
    FOREIGN KEY (folder_id, org_id) REFERENCES folders (id, org_id);
CREATE INDEX idx_projects_folder ON projects (folder_id) WHERE folder_id IS NOT NULL;

-- The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
JOIN (VALUES
    ('owner', 'folder.create'), ('owner', 'folder.update'), ('owner', 'folder.delete'),
    ('admin', 'folder.create'), ('admin', 'folder.update'), ('admin', 'folder.delete'),
    ('member', 'folder.create'), ('member', 'folder.update')
) AS p (role_key, permission) ON p.role_key = r.key
WHERE r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission IN ('folder.create', 'folder.update', 'folder.delete');

ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_folder_id_fkey;
DROP INDEX IF EXISTS idx_projects_folder;
ALTER TABLE projects DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS folders;
