-- +goose Up
-- A component has at most one live binding of each kind. Every reader holds
-- one value per kind (component_metrics_current.metrics is keyed by kind),
-- so a second binding of the same kind only ever overwrote the first and
-- made the project's KPI cards count whichever synced last. Two repos are
-- two components.
--
-- Production did hold duplicates: the legacy import (2026-09-28) bound
-- each AlurKerja service's -dev and -release Harbor images (and one
-- -new-release) to the same component, 33 Trivy bindings on 16 components,
-- all disabled. Before the key can exist, every component keeps one
-- binding per kind and the others are retired the way a delete retires
-- them (deleted_at set, enabled off; snapshots and runs stay, and clearing
-- deleted_at brings one back). The one kept is, in order: an enabled one;
-- a -release image over a -new-release one over anything else (-dev); the
-- most recent successful sync; the newest.
WITH ranked AS (
    SELECT b.id,
           row_number() OVER (
               PARTITION BY b.component_id, b.kind
               ORDER BY b.enabled DESC,
                        CASE WHEN b.external_key LIKE '%-new-release' THEN 1
                             WHEN b.external_key LIKE '%-release' THEN 0
                             ELSE 2 END,
                        (SELECT max(r.finished_at) FROM sync_runs r
                         WHERE r.binding_id = b.id AND r.status IN ('ok', 'partial')) DESC NULLS LAST,
                        b.created_at DESC
           ) AS rank
    FROM metric_bindings b
    WHERE b.deleted_at IS NULL
)
UPDATE metric_bindings b
SET deleted_at = now(), enabled = false
FROM ranked
WHERE ranked.id = b.id AND ranked.rank > 1;

DROP INDEX uq_metric_bindings_target;
CREATE UNIQUE INDEX uq_metric_bindings_kind ON metric_bindings (component_id, kind)
    WHERE deleted_at IS NULL;

-- +goose Down
-- The retired duplicates stay retired: which ones they were is not
-- recorded here (their deleted_at is this migration's time).
DROP INDEX uq_metric_bindings_kind;
CREATE UNIQUE INDEX uq_metric_bindings_target ON metric_bindings (component_id, kind, external_key)
    WHERE deleted_at IS NULL;
