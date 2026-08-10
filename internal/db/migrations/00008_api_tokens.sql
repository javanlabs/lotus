-- +goose Up
-- Task 1.8: two principal kinds share one table. A Personal Access Token
-- has user_id set and org_id NULL — it acts as its owner in whatever
-- organization they're a member of, resolved per request from the Host,
-- never baked in at creation. An Organization Service Token has org_id set
-- and user_id NULL — it survives its creator's departure, which is the
-- whole point of it existing (the old design's token died with its
-- creator's account, silently breaking every CI pipeline that used it).
--
-- This table is control-plane, not tenant-plane, even though service
-- tokens carry an org_id: a PAT's org_id is NULL by design (it isn't
-- scoped to one organization), and RLS's FORCE policy of "org_id = current
-- org" would hide every PAT row unconditionally. Object-level scoping for
-- service tokens is instead enforced by the composite FK below plus the
-- application check in task 1.8's middleware (a token whose org_id doesn't
-- match the request's resolved org is rejected before anything else runs).
CREATE TABLE api_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL CHECK (kind IN ('pat', 'service')),
    user_id        uuid REFERENCES users (id) ON DELETE CASCADE,
    org_id         uuid REFERENCES organizations (id) ON DELETE CASCADE,
    name           text NOT NULL,
    token_hash     text NOT NULL UNIQUE,
    token_prefix   text NOT NULL,
    scopes         text[] NOT NULL DEFAULT '{}',
    project_id     uuid,
    component_id   uuid,
    ip_allowlist   text[],
    expires_at     timestamptz,
    -- Nullable + SET NULL, deliberately not NOT NULL/CASCADE like user_id
    -- above: created_by is provenance ("who set this up"), not ownership.
    -- A service token is owned by the organization (org_id, ON DELETE
    -- CASCADE) — losing track of which admin created it when that admin's
    -- account is later removed must not take the token, and every CI
    -- pipeline depending on it, down too. That was the old design's bug.
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    last_used_at   timestamptz,
    last_used_ip   text,
    revoked_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- Exactly one of user_id (PAT) / org_id (service) is set, matching the
    -- kind column, so a row can never be ambiguous about which principal
    -- shape it is.
    CHECK (
        (kind = 'pat'     AND user_id IS NOT NULL AND org_id IS NULL) OR
        (kind = 'service' AND org_id IS NOT NULL AND user_id IS NULL)
    ),
    -- Project/component pinning is a service-token-only feature (task
    -- 1.8's own scope), and both must belong to the token's own org_id —
    -- the composite FK is automatically satisfied (skipped) when org_id is
    -- NULL, which is exactly the PAT case where pinning never applies.
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id),
    FOREIGN KEY (component_id, org_id) REFERENCES components (id, org_id)
);
CREATE INDEX idx_api_tokens_user_id ON api_tokens (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_api_tokens_org_id ON api_tokens (org_id) WHERE org_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS api_tokens;
