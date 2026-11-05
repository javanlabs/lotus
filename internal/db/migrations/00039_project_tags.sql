-- +goose Up
-- Tags group an organization's projects; a project carries any number
-- (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §5).
-- They replace folders in the UI. The folder tables and projects.folder_id
-- stay exactly as they are; only the UI stops reading and writing them.
--
-- The composite (id, org_id) keys follow the components and folders
-- pattern: a project can only carry a tag from its own organization, even
-- through a bug that forgot a scope.
CREATE TABLE tags (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 40),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id)
);

-- Names are shown as written but unique ignoring case, so `project` can
-- never become a second PROJECT.
CREATE UNIQUE INDEX tags_org_name ON tags (org_id, lower(name));

CREATE TABLE project_tags (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    tag_id      uuid NOT NULL,
    PRIMARY KEY (project_id, tag_id),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id, org_id) REFERENCES tags (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_project_tags_tag ON project_tags (tag_id);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['tags', 'project_tags'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON tags, project_tags TO lotus_app;

-- Production's two top-level folders become the two launch tags: ITSD's
-- projects are PROJECT, PRODUCT's are PRODUCT. Nothing else is tagged, and
-- a deleted folder or project contributes nothing. This runs as the
-- migration role, which has BYPASSRLS (deploy/bootstrap.sh).
INSERT INTO tags (org_id, name)
SELECT DISTINCT f.org_id, m.tag FROM folders f
JOIN (VALUES ('ITSD', 'PROJECT'), ('PRODUCT', 'PRODUCT')) AS m (folder, tag) ON lower(f.name) = lower(m.folder)
WHERE f.deleted_at IS NULL AND f.parent_id IS NULL;

INSERT INTO project_tags (org_id, project_id, tag_id)
SELECT p.org_id, p.id, t.id FROM projects p
JOIN folders f ON f.id = p.folder_id AND f.deleted_at IS NULL AND f.parent_id IS NULL
JOIN (VALUES ('ITSD', 'PROJECT'), ('PRODUCT', 'PRODUCT')) AS m (folder, tag) ON lower(f.name) = lower(m.folder)
JOIN tags t ON t.org_id = p.org_id AND lower(t.name) = lower(m.tag)
WHERE p.deleted_at IS NULL;

-- The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
JOIN (VALUES ('owner', 'project.tag'), ('admin', 'project.tag')) AS p (role_key, permission) ON p.role_key = r.key
WHERE r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission = 'project.tag';
DROP TABLE IF EXISTS project_tags;
DROP TABLE IF EXISTS tags;
