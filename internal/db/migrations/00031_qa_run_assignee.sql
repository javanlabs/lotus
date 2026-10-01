-- +goose Up
-- A run's assignee: the lead tester answerable for the run as a whole, and
-- the default for its items. Items still carry their own assignee_id (who
-- should run that case); the run's is what an item without a tester of its
-- own gets at creation, and what changing the lead moves (only the items
-- that still followed the previous lead). Nullable: a run need not have one.
ALTER TABLE qa_runs ADD COLUMN assignee_id uuid REFERENCES users (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE qa_runs DROP COLUMN IF EXISTS assignee_id;
