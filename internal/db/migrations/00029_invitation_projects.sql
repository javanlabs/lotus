-- +goose Up
-- A guest invitation names the projects the guest will see
-- (docs/superpowers/plans/2026-09-27-qa-phase-7.md). Accepting it lists
-- the guest on them in project_members. Empty for every other role.
ALTER TABLE invitations ADD COLUMN project_ids uuid[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE invitations DROP COLUMN IF EXISTS project_ids;
