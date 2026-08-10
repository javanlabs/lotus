-- +goose Up
-- Task 2.3's database-level safety net, behind Go's own ValidateConfig
-- (internal/integrations) — not the source of truth, just what stops a
-- bad row from being written by any path that isn't the validated one.
ALTER TABLE metric_bindings
    ADD CONSTRAINT metric_bindings_kind_check CHECK (kind IN ('gitlab', 'sonarqube', 'trivy', 'sentry'));

-- Sentry's mode must be explicit (task 2.3: never inferred from which
-- fields are filled — see internal/integrations/descriptor_sentry.go's
-- doc comment for the incident this replaces), and service_tag_value is
-- mandatory specifically when mode is "shared".
ALTER TABLE metric_bindings
    ADD CONSTRAINT metric_bindings_sentry_mode_check
    CHECK (kind <> 'sentry' OR (config ->> 'mode') IN ('shared', 'dedicated'));

ALTER TABLE metric_bindings
    ADD CONSTRAINT metric_bindings_sentry_shared_tag_check
    CHECK (kind <> 'sentry' OR (config ->> 'mode') <> 'shared' OR (config ->> 'service_tag_value') IS NOT NULL);

-- +goose Down
ALTER TABLE metric_bindings
    DROP CONSTRAINT IF EXISTS metric_bindings_kind_check,
    DROP CONSTRAINT IF EXISTS metric_bindings_sentry_mode_check,
    DROP CONSTRAINT IF EXISTS metric_bindings_sentry_shared_tag_check;
