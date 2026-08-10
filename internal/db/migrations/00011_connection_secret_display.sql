-- +goose Up
-- Task 2.2's write-only UI contract needs two more columns than migration
-- 00003 anticipated: the last 4 characters of the credential (not
-- sensitive on its own — doesn't help reconstruct the secret — and is the
-- standard "yes, something is configured, here's roughly which one" UI
-- affordance) and when it was last set. Added as its own migration rather
-- than editing 00003 or 00010: both have already been applied to a real
-- database (00010 to this repo's own dev DB) by the time this need was
-- discovered.
ALTER TABLE integration_connections
    ADD COLUMN secret_last4      text,
    ADD COLUMN secret_updated_at timestamptz;

-- +goose Down
ALTER TABLE integration_connections
    DROP COLUMN IF EXISTS secret_last4,
    DROP COLUMN IF EXISTS secret_updated_at;
