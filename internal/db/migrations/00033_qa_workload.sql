-- +goose Up
-- QC Workload: the cross-project monthly recap of who tested what (/qa/workload).
-- Owners and admins only. The documented copy of the Go permission matrix
-- (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix keeps them equal).
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'qa.workload.read'
FROM roles r
WHERE r.org_id IS NULL AND r.key IN ('owner', 'admin')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission = 'qa.workload.read';
