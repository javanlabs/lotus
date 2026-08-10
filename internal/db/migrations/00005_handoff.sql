-- +goose Up
-- Task 1.2 step 1: the handshake created on the tenant host, before any
-- auth happens, carrying {org_id, return_path, verifier_hash} across the
-- redirect to the auth host. Short-lived (120s) by design — it only needs
-- to survive one browser round trip. Distinct from auth_tickets (migration
-- 00002), which is the short-lived single-use *code* minted afterward,
-- once the auth host has actually authorized the user for this org.
CREATE TABLE handoff_handshakes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    return_path   text NOT NULL,
    verifier_hash text NOT NULL,
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_handoff_handshakes_expires_at ON handoff_handshakes (expires_at);

-- +goose Down
DROP TABLE IF EXISTS handoff_handshakes;
