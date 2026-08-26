-- +goose Up
-- Magic-link login: a single-use, short-lived token emailed to (or, in dev,
-- shown to) someone who already has a membership.
--
-- id is sha256(raw token) in hex, never the raw value — same rule the
-- sessions and invitations tables already follow, so a database read can
-- never yield a usable credential.
--
-- org_id is stored on the token rather than resolved at redemption time:
-- the token grants a session in one specific organization, and pinning it
-- at issue time means a membership revoked in between cannot be
-- side-stepped by redeeming against a different org.
CREATE TABLE login_tokens (
    id          text PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    return_path text NOT NULL DEFAULT '/',
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz
);

-- Supports the per-user rate limit's "how many issued recently" count.
CREATE INDEX idx_login_tokens_user_created ON login_tokens (user_id, created_at DESC);

-- +goose Down
DROP TABLE login_tokens;
