-- +goose Up
-- A component has at most one live binding of each kind. Every reader holds
-- one value per kind (component_metrics_current.metrics is keyed by kind),
-- so a second binding of the same kind only ever overwrote the first and
-- made the project's KPI cards count whichever synced last. Two repos are
-- two components. Production held no duplicates when this shipped
-- (checked 2026-10-04); on a database that does, this fails and names the
-- index, and the duplicates have to be retired by hand first.
DROP INDEX uq_metric_bindings_target;
CREATE UNIQUE INDEX uq_metric_bindings_kind ON metric_bindings (component_id, kind)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX uq_metric_bindings_kind;
CREATE UNIQUE INDEX uq_metric_bindings_target ON metric_bindings (component_id, kind, external_key)
    WHERE deleted_at IS NULL;
