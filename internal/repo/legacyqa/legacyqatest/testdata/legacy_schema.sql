DROP SCHEMA IF EXISTS legacy CASCADE;
CREATE SCHEMA legacy;
SET search_path = legacy;

CREATE TABLE users (id uuid PRIMARY KEY, email varchar NOT NULL, first_name varchar NOT NULL DEFAULT '',
  last_name varchar NOT NULL DEFAULT '', user_type varchar NOT NULL, status_user boolean NOT NULL,
  created_at timestamp);
CREATE TABLE projects (id uuid PRIMARY KEY, title varchar NOT NULL, created_at timestamp, deleted_at timestamp);
CREATE TABLE project_assignees (id uuid PRIMARY KEY, project_id uuid, contributor_id uuid, deleted_at timestamp);
CREATE TABLE modules (id uuid PRIMARY KEY, module_name varchar, project_id uuid, parent_id uuid,
  "order" integer NOT NULL DEFAULT 0, is_archive boolean NOT NULL DEFAULT false, deleted_at timestamp,
  created_at timestamp, updated_at timestamp);
CREATE TABLE test_cases (id uuid PRIMARY KEY, case_code varchar, title text, precondition text, description text,
  project_id uuid, module_id uuid, priority varchar, tag varchar, is_archive boolean NOT NULL DEFAULT false,
  deleted_at timestamp, created_by uuid, created_at timestamp, updated_at timestamp);
CREATE TABLE steps (id uuid PRIMARY KEY, test_case_id uuid, title text, expected_result text, position_step integer);
CREATE TABLE test_plans (id uuid PRIMARY KEY, project_id uuid, plan_name text, assignment_time timestamp,
  expiration_time timestamp, is_archive boolean NOT NULL DEFAULT false, deleted_at timestamp, created_by uuid,
  created_at timestamp);
CREATE TABLE releases (id uuid PRIMARY KEY, project_id uuid, name varchar, start_date date, end_date date,
  is_archive boolean NOT NULL DEFAULT false, deleted_at timestamp, created_by uuid, created_at timestamp);
CREATE TABLE test_runs (id uuid PRIMARY KEY, project_id uuid, title varchar, description varchar, test_plan_id uuid,
  release_id uuid, version varchar, browser varchar, is_archive boolean NOT NULL DEFAULT false,
  deleted_at timestamp, created_by uuid, created_at timestamp);
CREATE TABLE test_run_details (id uuid PRIMARY KEY, test_run_id uuid, test_case_id uuid, assignee_id uuid,
  status varchar, updated_status_at timestamp, deleted_at timestamp, created_at timestamp);
CREATE TABLE results (id uuid PRIMARY KEY, test_run_detail_id uuid, status varchar, comment_test text,
  created_by uuid, created_at timestamp, deleted_at timestamp);
CREATE TABLE test_issues (id uuid PRIMARY KEY, project_id uuid, code_issue varchar, title text, description text,
  step_issue text, environment_issue varchar, release_id uuid, module_id uuid, assignee_issue uuid,
  created_by uuid, priority varchar, severity varchar, status varchar, deleted_at timestamp,
  created_at timestamp, updated_at timestamp);
CREATE TABLE test_run_detail_issues (id uuid PRIMARY KEY, test_run_detail_id uuid, test_issue_id uuid,
  deleted_at timestamp, created_at timestamp);
CREATE TABLE comments (id uuid PRIMARY KEY, test_issue_id uuid, comment_summary text, created_by uuid,
  deleted_at timestamp, created_at timestamp);
CREATE TABLE test_issue_logs (uuid uuid PRIMARY KEY, test_issue_id uuid, status_before varchar,
  status_after varchar, updated_by uuid, created_at timestamp);
CREATE TABLE minio (id uuid PRIMARY KEY, name varchar, path varchar, size double precision, relation_id uuid,
  relation_model varchar, created_at timestamp);
