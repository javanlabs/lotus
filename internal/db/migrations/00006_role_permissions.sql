-- +goose Up
-- The task 1.4 permission matrix, for the record: role_permissions exists
-- (migration 00002) specifically to hold this, and seeding it here keeps
-- the schema self-documenting and ready for whenever custom per-org roles
-- (out of scope for v1) need to read it. The live enforcement path in Go
-- (internal/authz.rolePermissions) is a compile-time copy of the same
-- data, not a read of this table — a DB round trip on every permission
-- check isn't worth it while the 4 system roles are fixed and can only
-- change via a migration anyway. If that ever drifts, this table is the
-- one to trust.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
CROSS JOIN (VALUES
    ('dashboard.read'), ('project.create'), ('project.update'), ('project.delete'),
    ('component.create'), ('component.update'), ('component.delete'),
    ('metric.bind'), ('metric.unbind'),
    ('connection.read'), ('connection.secret.write'),
    ('sync.trigger'), ('sync.schedule'),
    ('member.invite'), ('member.remove'), ('member.role.change'),
    ('org.settings.update'), ('org.domain.verify'), ('org.slug.change'), ('org.sso.configure'),
    ('org.delete'), ('org.transfer_ownership'),
    ('token.manage.own'), ('token.manage.org'), ('audit.read')
) AS p(permission)
WHERE r.org_id IS NULL
  AND r.key = 'owner';

INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
CROSS JOIN (VALUES
    ('dashboard.read'), ('project.create'), ('project.update'), ('project.delete'),
    ('component.create'), ('component.update'), ('component.delete'),
    ('metric.bind'), ('metric.unbind'),
    ('connection.read'), ('connection.secret.write'),
    ('sync.trigger'), ('sync.schedule'),
    ('member.invite'), ('member.remove'), ('member.role.change'),
    ('org.settings.update'), ('org.domain.verify'),
    ('token.manage.own'), ('token.manage.org'), ('audit.read')
) AS p(permission)
WHERE r.org_id IS NULL
  AND r.key = 'admin';

INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
CROSS JOIN (VALUES
    ('dashboard.read'), ('project.create'), ('project.update'),
    ('component.create'), ('component.update'),
    ('metric.bind'), ('metric.unbind'),
    ('connection.read'),
    ('sync.trigger'),
    ('token.manage.own')
) AS p(permission)
WHERE r.org_id IS NULL
  AND r.key = 'member';

INSERT INTO role_permissions (role_id, permission)
SELECT r.id, p.permission
FROM roles r
CROSS JOIN (VALUES
    ('dashboard.read'), ('token.manage.own')
) AS p(permission)
WHERE r.org_id IS NULL
  AND r.key = 'viewer';

-- +goose Down
DELETE FROM role_permissions;
