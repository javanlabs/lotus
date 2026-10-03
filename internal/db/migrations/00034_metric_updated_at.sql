-- +goose Up
-- Each kind inside component_metrics_current.metrics now carries its own
-- updated_at (the upsert stamps it on every successful collect), so the
-- project page can say which integration stopped syncing. Existing rows
-- get one from the kind's last successful sync run, or, when retention
-- has already removed those runs, from the row's own updated_at — the
-- newest any kind could be, so a kind that is already stale shows as
-- stale within a day either way.
UPDATE component_metrics_current cm
SET metrics = (
    SELECT jsonb_object_agg(
        k.key,
        k.value || jsonb_build_object('updated_at', COALESCE(
            (SELECT max(r.finished_at)
             FROM sync_runs r
             JOIN metric_bindings b ON b.id = r.binding_id
             WHERE b.component_id = cm.component_id
               AND b.kind = k.key
               AND r.status IN ('ok', 'partial')
               AND r.finished_at <= cm.updated_at),
            cm.updated_at)))
    FROM jsonb_each(cm.metrics) k
)
WHERE cm.metrics <> '{}'::jsonb;

-- +goose Down
UPDATE component_metrics_current cm
SET metrics = (
    SELECT jsonb_object_agg(k.key, k.value - 'updated_at')
    FROM jsonb_each(cm.metrics) k
)
WHERE cm.metrics <> '{}'::jsonb;
