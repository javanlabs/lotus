-- +goose Up
-- Fixes a real bug in migration 00003's RLS policies, found while building
-- task 2.1: current_setting('app.org_id', true) is documented there (and in
-- tenant.Scope) to return NULL — making "org_id = NULL" fail closed to zero
-- rows — whenever a request forgets to scope its query. That's true only
-- the *first* time a physical connection is used. Once any transaction on
-- that connection has run set_config('app.org_id', ..., true) (i.e. gone
-- through tenant.Scope.Tx even once) and later committed, Postgres leaves
-- the placeholder GUC defined-but-empty rather than reverting it to
-- undefined — current_setting keeps returning '' instead of NULL from then
-- on for the lifetime of that connection. '' ::uuid is not NULL, it's a
-- cast error, so an unscoped query on an already-used connection would
-- 500 instead of yielding zero rows. In a pooled connection, "already used
-- at least once" describes essentially every connection after the first
-- real request — verified directly against Postgres (BEGIN; SELECT
-- set_config('app.org_id', '<uuid>', true); COMMIT; SELECT
-- current_setting('app.org_id', true) IS NULL — returns false, not true).
--
-- NULLIF(..., '') collapses that leftover empty string back to NULL before
-- the ::uuid cast, restoring the zero-rows-not-an-error guarantee for real.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['projects', 'components', 'integration_connections', 'metric_bindings', 'discovered_resources']
    LOOP
        EXECUTE format('DROP POLICY tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)',
            t
        );
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['projects', 'components', 'integration_connections', 'metric_bindings', 'discovered_resources']
    LOOP
        EXECUTE format('DROP POLICY tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = current_setting(''app.org_id'', true)::uuid) WITH CHECK (org_id = current_setting(''app.org_id'', true)::uuid)',
            t
        );
    END LOOP;
END
$$;
-- +goose StatementEnd
