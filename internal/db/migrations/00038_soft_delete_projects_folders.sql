-- +goose Up
-- Deleting a project or folder from the UI only hides it: the history under it
-- stays, and it can be brought back by hand. A deleted one no longer holds its
-- slug or name, so both unique indexes cover only what is still live.
ALTER TABLE projects ADD COLUMN deleted_at timestamptz;
ALTER TABLE projects DROP CONSTRAINT projects_org_id_slug_key;
CREATE UNIQUE INDEX projects_org_id_slug_key ON projects (org_id, slug) WHERE deleted_at IS NULL;

ALTER TABLE folders ADD COLUMN deleted_at timestamptz;
DROP INDEX folders_sibling_name;
CREATE UNIQUE INDEX folders_sibling_name
    ON folders (org_id, parent_id, lower(name)) NULLS NOT DISTINCT WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX folders_sibling_name;
CREATE UNIQUE INDEX folders_sibling_name
    ON folders (org_id, parent_id, lower(name)) NULLS NOT DISTINCT;
ALTER TABLE folders DROP COLUMN deleted_at;

DROP INDEX projects_org_id_slug_key;
ALTER TABLE projects ADD CONSTRAINT projects_org_id_slug_key UNIQUE (org_id, slug);
ALTER TABLE projects DROP COLUMN deleted_at;
