-- +goose Up
-- The status vocabulary loses its middle tier. "warn" existed as an
-- at-risk state between pass and fail; it was removed because a reader
-- has to act on a component or not, and a maybe-tier mostly deferred that
-- decision — a component with two critical CVEs is not partially fine.
--
-- Existing rows are backfilled to 'fail', not 'pass': collapsing upward
-- would make real risk invisible, and the whole point of the projection
-- is that a failing component is visible. This is one-way. The
-- pass/warn/fail distinction is not recoverable from this table
-- afterwards; the underlying metric_facts are untouched, so a rollup can
-- always be recomputed from them.
--
-- Order matters: the rows have to satisfy the new constraint before it is
-- added, so backfill first, then swap the CHECK.
UPDATE component_metrics_current SET status = 'fail' WHERE status = 'warn';

ALTER TABLE component_metrics_current DROP CONSTRAINT component_metrics_current_status_check;
ALTER TABLE component_metrics_current ADD CONSTRAINT component_metrics_current_status_check
    CHECK (status IN ('pass', 'fail', 'unknown'));

-- +goose Down
-- Widening the constraint again is safe; the rows that used to say 'warn'
-- stay 'fail', because which ones they were is no longer recorded.
ALTER TABLE component_metrics_current DROP CONSTRAINT component_metrics_current_status_check;
ALTER TABLE component_metrics_current ADD CONSTRAINT component_metrics_current_status_check
    CHECK (status IN ('pass', 'warn', 'fail', 'unknown'));
