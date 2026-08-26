-- +goose Up
-- Single-mode bootstrap.
--
-- `lotus init` creates the organization ahead of first boot, but cannot
-- create its Owner: memberships.user_id is NOT NULL and nobody has logged
-- in yet. So the org is inserted with zero memberships — legal, because
-- enforce_min_one_owner fires BEFORE UPDATE OR DELETE, never on INSERT —
-- and the pending owner's email is parked here until the first login with
-- a matching, verified address claims it.
--
-- The boolean primary key with a CHECK is the "at most one row" idiom: a
-- second `lotus init` fails on the primary key even if the application's
-- own check is somehow bypassed.
CREATE TABLE instance_bootstrap (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    owner_email text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz,
    consumed_by uuid REFERENCES users (id)
);

-- +goose Down
DROP TABLE instance_bootstrap;
