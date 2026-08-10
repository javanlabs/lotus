-- +goose Up
-- Task 2.2: envelope encryption for per-organization integration
-- credentials, plus the audit_events table its base_url-change rule needs
-- (audit.read has existed as a permission since task 1.4's matrix; nothing
-- wrote to it until now).
--
-- One wrapped DEK per organization, stored on organizations itself rather
-- than a separate table: there is exactly one active DEK per org at a
-- time (KEK rotation re-wraps it in place, see cmd/lotus rewrap-keys), so
-- a join table would only add an extra lookup for no extra expressiveness.
-- Control-plane, so — like organizations itself — not subject to RLS.
ALTER TABLE organizations
    ADD COLUMN dek_ciphertext  bytea,
    ADD COLUMN dek_nonce       bytea,
    ADD COLUMN dek_kek_version integer;

CREATE TABLE audit_events (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    actor_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    action         text NOT NULL,
    target_kind    text NOT NULL,
    target_id      uuid,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_events_org_created ON audit_events (org_id, created_at DESC);

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY');
    EXECUTE format('ALTER TABLE audit_events FORCE ROW LEVEL SECURITY');
    EXECUTE format(
        'CREATE POLICY tenant_isolation ON audit_events USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)'
    );
END
$$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON audit_events TO lotus_app;

-- +goose Down
DROP TABLE IF EXISTS audit_events;
ALTER TABLE organizations
    DROP COLUMN IF EXISTS dek_ciphertext,
    DROP COLUMN IF EXISTS dek_nonce,
    DROP COLUMN IF EXISTS dek_kek_version;
