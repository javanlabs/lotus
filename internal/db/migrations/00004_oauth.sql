-- +goose Up
-- Task 1.1: a user can be created with no email at all (e.g. GitHub gives
-- neither a public email nor a verified one) — "create user without
-- verified email and ask them to fill it in" is explicit product behavior,
-- not an edge case to reject. Postgres UNIQUE already treats NULL as
-- distinct from every other NULL, so multiple emailless users don't
-- collide; only the CHECK needs relaxing to allow NULL through.
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users DROP CONSTRAINT users_email_check;
ALTER TABLE users ADD CONSTRAINT users_email_check CHECK (email IS NULL OR email = lower(email));

-- OAuth state: single-use, double-submitted against the __Host-lotus_oauth
-- cookie (task 1.1). Storing state_hash rather than the raw value for
-- consistency with every other single-use token in this schema
-- (auth_tickets.token_hash, invitations.token_hash, ...) — the row's
-- security doesn't actually depend on DB secrecy here (the cookie
-- double-submit is what matters), but hashing costs nothing and keeps the
-- pattern uniform.
CREATE TABLE oauth_states (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    state_hash    text NOT NULL UNIQUE,
    provider      text NOT NULL CHECK (provider IN ('google', 'github')),
    code_verifier text NOT NULL, -- PKCE verifier; needed in plaintext at exchange time
    return_path   text NOT NULL DEFAULT '/',
    expires_at    timestamptz NOT NULL,
    consumed_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_oauth_states_expires_at ON oauth_states (expires_at);

-- +goose Down
DROP TABLE IF EXISTS oauth_states;
ALTER TABLE users DROP CONSTRAINT users_email_check;
ALTER TABLE users ADD CONSTRAINT users_email_check CHECK (email = lower(email));
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
