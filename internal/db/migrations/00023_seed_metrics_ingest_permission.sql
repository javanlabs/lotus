-- +goose Up
-- metrics.ingest was added to the Go permission matrix by task 3.4 (the
-- push-ingest endpoint) but never to this table, which migration 00006
-- seeds as the documented copy of the same matrix. Nothing broke —
-- enforcement reads the Go map — but the two copies had silently
-- diverged, which is exactly what the tension about this table warned
-- would happen with nothing comparing them.
--
-- The comparison now exists (internal/db.TestRolePermissionsSeedMatchesTheGoMatrix,
-- which is what found this), so the next divergence fails a test instead
-- of waiting to be noticed.
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, 'metrics.ingest'
FROM roles r
WHERE r.org_id IS NULL
  AND r.key IN ('owner', 'admin', 'member')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.org_id IS NULL
  AND rp.permission = 'metrics.ingest';
