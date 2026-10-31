-- +goose Up
-- A failed sign-in freezes every binding on the connection by turning it off
-- (enabled = false), which is the same state someone switching one off leaves.
-- With nothing to tell them apart, a new credential could not resume the
-- frozen ones without also resuming those turned off on purpose, so nothing
-- resumed: production's Harbor bindings stayed off for a month after its
-- credential expired, and replacing the credential would not have helped.
ALTER TABLE metric_bindings ADD COLUMN frozen_at timestamptz;

-- Whatever is off on a connection that is still failing to sign in was frozen
-- by that failure as far as anyone can now tell. If one was off on purpose,
-- the worst outcome is that it resumes once when the credential is replaced.
UPDATE metric_bindings b
SET frozen_at = now()
FROM integration_connections c
WHERE c.id = b.connection_id AND c.status = 'auth_failed'
  AND NOT b.enabled AND b.deleted_at IS NULL;

-- +goose Down
ALTER TABLE metric_bindings DROP COLUMN frozen_at;
