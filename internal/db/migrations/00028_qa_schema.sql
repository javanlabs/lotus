-- +goose Up
-- QA module schema (docs/superpowers/specs/2026-09-27-qa-module-design.md §4).
-- Same tenant rules as folders (00026): org_id on every row, composite
-- (x_id, org_id) foreign keys, RLS forced, NO ACTION rather than RESTRICT.
--
-- Four keys are DEFERRABLE INITIALLY DEFERRED: run items and scenario items
-- -> cases, plan scenarios and sign-offs -> scenarios. Deleting a project
-- (or an organization) removes both sides of each in one cascade, but at
-- different depths: a case is one step from its project, a run item three
-- (project -> plan -> run -> item). A plain NO ACTION check fires when the
-- case is deleted, before the item's cascade has run, and aborts the delete.
-- Deferring the check to commit lets the whole cascade finish first. A
-- genuine dangling reference (hard-deleting a case still used by a run) is
-- still refused, at COMMIT instead of at the DELETE.

CREATE TABLE qa_sections (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    parent_id   uuid,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 200),
    sort_order  integer NOT NULL DEFAULT 0,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id, org_id) REFERENCES qa_sections (id, org_id)
);
CREATE UNIQUE INDEX qa_sections_sibling_name
    ON qa_sections (project_id, parent_id, lower(name)) NULLS NOT DISTINCT;

CREATE TABLE qa_cases (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id    uuid NOT NULL,
    section_id    uuid NOT NULL,
    key           integer NOT NULL CHECK (key > 0),
    title         text NOT NULL CHECK (btrim(title) <> '' AND char_length(title) <= 500),
    precondition  text NOT NULL DEFAULT '',
    notes         text NOT NULL DEFAULT '',
    steps         jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps) = 'array'),
    priority      text CHECK (priority IN ('high', 'medium', 'low')),
    negative      boolean NOT NULL DEFAULT false,
    version       integer NOT NULL DEFAULT 1,
    archived_at   timestamptz,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    UNIQUE (project_id, key),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (section_id, org_id) REFERENCES qa_sections (id, org_id)
);
CREATE INDEX idx_qa_cases_section ON qa_cases (section_id);

CREATE TABLE qa_scenarios (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 200),
    phase       text NOT NULL CHECK (phase IN ('smoke', 'sit', 'uat', 'regression', 'other')),
    description text NOT NULL DEFAULT '',
    archived_at timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);

CREATE TABLE qa_scenario_groups (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    scenario_id uuid NOT NULL,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 200),
    position    integer NOT NULL DEFAULT 0,
    UNIQUE (id, org_id),
    FOREIGN KEY (scenario_id, org_id) REFERENCES qa_scenarios (id, org_id) ON DELETE CASCADE
);

CREATE TABLE qa_scenario_items (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    scenario_id uuid NOT NULL,
    group_id    uuid,
    case_id     uuid NOT NULL,
    position    integer NOT NULL DEFAULT 0,
    UNIQUE (id, org_id),
    UNIQUE (scenario_id, case_id),
    FOREIGN KEY (scenario_id, org_id) REFERENCES qa_scenarios (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (group_id, org_id) REFERENCES qa_scenario_groups (id, org_id) ON DELETE SET NULL (group_id),
    FOREIGN KEY (case_id, org_id) REFERENCES qa_cases (id, org_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE qa_plans (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id  uuid NOT NULL,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 200),
    phase       text NOT NULL CHECK (phase IN ('smoke', 'sit', 'uat', 'regression', 'other')),
    environment text NOT NULL DEFAULT '',
    build       text NOT NULL DEFAULT '',
    starts_on   date,
    ends_on     date,
    closed_at   timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);

CREATE TABLE qa_plan_scenarios (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id     uuid NOT NULL,
    scenario_id uuid NOT NULL,
    PRIMARY KEY (plan_id, scenario_id),
    FOREIGN KEY (plan_id, org_id) REFERENCES qa_plans (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (scenario_id, org_id) REFERENCES qa_scenarios (id, org_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE qa_runs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id     uuid NOT NULL,
    name        text NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 200),
    cycle       integer NOT NULL DEFAULT 1 CHECK (cycle >= 1),
    retest_of   uuid,
    notes       text NOT NULL DEFAULT '',
    closed_at   timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (plan_id, org_id) REFERENCES qa_plans (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (retest_of, org_id) REFERENCES qa_runs (id, org_id) ON DELETE SET NULL (retest_of)
);
CREATE INDEX idx_qa_runs_plan ON qa_runs (plan_id);

CREATE TABLE qa_run_items (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    run_id        uuid NOT NULL,
    case_id       uuid NOT NULL,
    group_name    text NOT NULL DEFAULT '',
    position      integer NOT NULL DEFAULT 0,
    title         text NOT NULL,
    precondition  text NOT NULL DEFAULT '',
    steps         jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps) = 'array'),
    case_version  integer NOT NULL DEFAULT 1,
    assignee_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    status        text NOT NULL DEFAULT 'untested'
                  CHECK (status IN ('untested', 'passed', 'failed', 'blocked', 'skipped')),
    status_at     timestamptz,
    executed_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (id, org_id),
    UNIQUE (run_id, case_id),
    FOREIGN KEY (run_id, org_id) REFERENCES qa_runs (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (case_id, org_id) REFERENCES qa_cases (id, org_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_qa_run_items_assignee ON qa_run_items (assignee_id) WHERE assignee_id IS NOT NULL;

CREATE TABLE qa_results (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    item_id     uuid NOT NULL,
    status      text NOT NULL CHECK (status IN ('passed', 'failed', 'blocked', 'skipped')),
    comment     text NOT NULL DEFAULT '',
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (item_id, org_id) REFERENCES qa_run_items (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_qa_results_item ON qa_results (item_id, created_at);

CREATE TABLE qa_issues (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id          uuid NOT NULL,
    key                 integer NOT NULL CHECK (key > 0),
    title               text NOT NULL CHECK (btrim(title) <> '' AND char_length(title) <= 500),
    description         text NOT NULL DEFAULT '',
    steps_to_reproduce  text NOT NULL DEFAULT '',
    severity            text NOT NULL CHECK (severity IN ('blocker', 'critical', 'major', 'minor', 'trivial')),
    status              text NOT NULL DEFAULT 'new'
                        CHECK (status IN ('new', 'open', 'fixed', 'closed', 'invalid', 'duplicate', 'reopened')),
    environment         text NOT NULL DEFAULT '',
    plan_id             uuid,
    section_id          uuid,
    assignee_id         uuid REFERENCES users (id) ON DELETE SET NULL,
    reported_by         uuid REFERENCES users (id) ON DELETE SET NULL,
    source              text NOT NULL DEFAULT 'internal' CHECK (source IN ('internal', 'client')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    UNIQUE (project_id, key),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (plan_id, org_id) REFERENCES qa_plans (id, org_id) ON DELETE SET NULL (plan_id),
    FOREIGN KEY (section_id, org_id) REFERENCES qa_sections (id, org_id) ON DELETE SET NULL (section_id)
);
CREATE INDEX idx_qa_issues_project_status ON qa_issues (project_id, status);

CREATE TABLE qa_issue_links (
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    item_id     uuid NOT NULL,
    issue_id    uuid NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, issue_id),
    FOREIGN KEY (item_id, org_id) REFERENCES qa_run_items (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (issue_id, org_id) REFERENCES qa_issues (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_qa_issue_links_issue ON qa_issue_links (issue_id);

CREATE TABLE qa_issue_events (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    issue_id    uuid NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('comment', 'status')),
    body        text NOT NULL DEFAULT '',
    status_from text,
    status_to   text,
    actor_id    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    CHECK (kind = 'comment' OR status_to IS NOT NULL),
    FOREIGN KEY (issue_id, org_id) REFERENCES qa_issues (id, org_id) ON DELETE CASCADE
);
CREATE INDEX idx_qa_issue_events_issue ON qa_issue_events (issue_id, created_at);

CREATE TABLE qa_attachments (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    result_id     uuid,
    issue_id      uuid,
    event_id      uuid,
    file_name     text NOT NULL CHECK (btrim(file_name) <> '' AND char_length(file_name) <= 255),
    content_type  text NOT NULL,
    size_bytes    bigint NOT NULL CHECK (size_bytes >= 0),
    storage_key   text NOT NULL,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    CONSTRAINT qa_attachments_one_owner CHECK (num_nonnulls(result_id, issue_id, event_id) = 1),
    FOREIGN KEY (result_id, org_id) REFERENCES qa_results (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (issue_id, org_id) REFERENCES qa_issues (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (event_id, org_id) REFERENCES qa_issue_events (id, org_id) ON DELETE CASCADE
);

CREATE TABLE qa_signoffs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id     uuid NOT NULL,
    scenario_id uuid,
    decision    text NOT NULL CHECK (decision IN ('accepted', 'accepted_with_notes', 'rejected')),
    note        text NOT NULL DEFAULT '',
    signed_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    signed_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (plan_id, org_id) REFERENCES qa_plans (id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (scenario_id, org_id) REFERENCES qa_scenarios (id, org_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE qa_share_links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    plan_id     uuid NOT NULL,
    token_hash  text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id),
    FOREIGN KEY (plan_id, org_id) REFERENCES qa_plans (id, org_id) ON DELETE CASCADE
);

CREATE TABLE qa_legacy_ids (
    org_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    legacy_table  text NOT NULL,
    legacy_id     uuid NOT NULL,
    new_id        uuid NOT NULL,
    imported_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, legacy_table, legacy_id)
);

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'qa_sections', 'qa_cases', 'qa_scenarios', 'qa_scenario_groups', 'qa_scenario_items',
        'qa_plans', 'qa_plan_scenarios', 'qa_runs', 'qa_run_items', 'qa_results',
        'qa_issues', 'qa_issue_links', 'qa_issue_events', 'qa_attachments',
        'qa_signoffs', 'qa_share_links', 'qa_legacy_ids'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid) WITH CHECK (org_id = NULLIF(current_setting(''app.org_id'', true), '''')::uuid)',
            t
        );
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO lotus_app', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS qa_legacy_ids, qa_share_links, qa_signoffs, qa_attachments,
    qa_issue_events, qa_issue_links, qa_issues, qa_results, qa_run_items, qa_runs,
    qa_plan_scenarios, qa_plans, qa_scenario_items, qa_scenario_groups, qa_scenarios,
    qa_cases, qa_sections;
