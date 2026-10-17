-- +goose Up
-- A component with no container image can still be scanned: Lotus clones
-- its GitLab repository and runs `trivy fs` over it (a "repository scan",
-- internal/collector/trivy/repo_scan.go). Its snapshots are source
-- 'repo'; digest holds the scanned commit SHA, as it holds the image
-- digest for a registry scan — in both cases, exactly what was scanned.
--
-- The source CHECK was created under different names as the table was
-- rebuilt (00016, then 00018's partitioned copy), so it is found by
-- definition rather than by name.
-- +goose StatementBegin
DO $$
DECLARE
    c text;
BEGIN
    FOR c IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'trivy_snapshots'::regclass AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%source%'
    LOOP
        EXECUTE format('ALTER TABLE trivy_snapshots DROP CONSTRAINT %I', c);
    END LOOP;
END $$;
-- +goose StatementEnd
ALTER TABLE trivy_snapshots ADD CONSTRAINT trivy_snapshots_source_check
    CHECK (source IN ('registry', 'push', 'repo'));

-- +goose Down
DELETE FROM trivy_snapshots WHERE source = 'repo';
ALTER TABLE trivy_snapshots DROP CONSTRAINT trivy_snapshots_source_check;
ALTER TABLE trivy_snapshots ADD CONSTRAINT trivy_snapshots_source_check
    CHECK (source IN ('registry', 'push'));
