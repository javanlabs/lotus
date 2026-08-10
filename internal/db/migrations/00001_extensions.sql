-- +goose Up
-- pgcrypto provides gen_random_uuid(), used as the default for every UUID
-- primary key from task 0.2 onward.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
