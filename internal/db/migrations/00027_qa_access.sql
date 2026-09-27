-- +goose Up
-- QA module access model (docs/superpowers/specs/2026-09-27-qa-module-design.md §5).
-- guest is a client doing UAT: it sees only the projects listed for it in
-- project_members, and has no dashboard.read.
INSERT INTO roles (org_id, key, name)
SELECT NULL, 'guest', 'Guest'
WHERE NOT EXISTS (SELECT 1 FROM roles WHERE org_id IS NULL AND key = 'guest');

CREATE TABLE project_members (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    UNIQUE (project_id, user_id),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_project_members_user ON project_members (user_id);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'ALTER TABLE project_members ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE project_members FORCE ROW LEVEL SECURITY';
    EXECUTE 'CREATE POLICY tenant_isolation ON project_members USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)';
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON project_members TO lotus_app;

-- The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
JOIN (VALUES
    ('owner', 'qa.read'), ('owner', 'qa.case.write'), ('owner', 'qa.plan.write'), ('owner', 'qa.execute'),
    ('owner', 'qa.issue.write'), ('owner', 'qa.issue.report'), ('owner', 'qa.signoff'), ('owner', 'qa.share'),
    ('owner', 'project_member.manage'),
    ('admin', 'qa.read'), ('admin', 'qa.case.write'), ('admin', 'qa.plan.write'), ('admin', 'qa.execute'),
    ('admin', 'qa.issue.write'), ('admin', 'qa.issue.report'), ('admin', 'qa.signoff'), ('admin', 'qa.share'),
    ('admin', 'project_member.manage'),
    ('member', 'qa.read'), ('member', 'qa.case.write'), ('member', 'qa.plan.write'), ('member', 'qa.execute'),
    ('member', 'qa.issue.write'), ('member', 'qa.issue.report'), ('member', 'qa.share'),
    ('viewer', 'qa.read'),
    ('guest', 'qa.read'), ('guest', 'qa.execute'), ('guest', 'qa.issue.report'), ('guest', 'qa.signoff')
) AS p (role_key, permission) ON p.role_key = r.key
WHERE r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND (rp.permission LIKE 'qa.%' OR rp.permission = 'project_member.manage');
DROP TABLE IF EXISTS project_members;
DELETE FROM roles WHERE org_id IS NULL AND key = 'guest';
