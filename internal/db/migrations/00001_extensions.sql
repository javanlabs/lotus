-- +goose Up
-- pgcrypto provides gen_random_uuid(), used as the default for every UUID
-- primary key from task 0.2 onward.
--
-- CREATE EXTENSION IF NOT EXISTS is not actually race-safe under real
-- concurrency (its existence check and the catalog insert aren't atomic) —
-- harmless in a single production deploy, but every test here migrates a
-- fresh schema against the same shared database, and Go test packages run
-- concurrently, so this races routinely. Swallowing the resulting
-- unique_violation is the fix; a genuine failure to create the extension
-- for any other reason still surfaces normally.
-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS pgcrypto;
    EXCEPTION WHEN unique_violation OR duplicate_object THEN NULL;
    END;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
