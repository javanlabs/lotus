-- +goose Up
-- Task 1.7 adds a decline action distinct from revoke: revoked_at means an
-- admin pulled the invite back; declined_at means the invitee said no.
-- Both are terminal states an invitation can reach, so both need their own
-- column rather than overloading one.
ALTER TABLE invitations ADD COLUMN declined_at timestamptz;

-- The "one pending invite per (org, email)" index (migration 00002)
-- predates decline and didn't exclude it — a declined invitation would
-- otherwise permanently block re-inviting that address, which defeats the
-- point of being able to decline and be invited again later.
DROP INDEX uq_invitations_pending;
CREATE UNIQUE INDEX uq_invitations_pending ON invitations (org_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL AND declined_at IS NULL;

-- +goose Down
DROP INDEX uq_invitations_pending;
CREATE UNIQUE INDEX uq_invitations_pending ON invitations (org_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
ALTER TABLE invitations DROP COLUMN declined_at;
