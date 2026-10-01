// Package legacyqa reads the legacy QA Tools database for `lotus import-qa`.
// It never writes: every connection runs with default_transaction_read_only.
package legacyqa

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type Reader struct{ db *sqlx.DB }

func Open(dsn string) (*Reader, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["timezone"] = "UTC"
	return &Reader{db: sqlx.NewDb(stdlib.OpenDB(*cfg), "pgx")}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

// DB exposes the connection for tests that assert it is read-only.
func (r *Reader) DB() *sqlx.DB { return r.db }

type User struct {
	ID        uuid.UUID `db:"id"`
	Email     string    `db:"email"`
	FirstName string    `db:"first_name"`
	LastName  string    `db:"last_name"`
	UserType  string    `db:"user_type"`
	Active    bool      `db:"status_user"`
}

func (r *Reader) Users(ctx context.Context) ([]User, error) {
	var out []User
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, email, first_name, last_name, user_type, status_user FROM users ORDER BY created_at, id`)
	return out, err
}

func (r *Reader) LastActivity(ctx context.Context) (map[uuid.UUID]time.Time, error) {
	var rows []struct {
		U uuid.UUID `db:"u"`
		T time.Time `db:"t"`
	}
	err := r.db.SelectContext(ctx, &rows, `
		SELECT u, max(t) AS t FROM (
			SELECT created_by AS u, created_at AS t FROM test_cases
			UNION ALL SELECT created_by, created_at FROM test_runs
			UNION ALL SELECT created_by, created_at FROM results
			UNION ALL SELECT created_by, created_at FROM test_issues
			UNION ALL SELECT created_by, created_at FROM comments
			UNION ALL SELECT updated_by, created_at FROM test_issue_logs
		) x WHERE u IS NOT NULL AND t IS NOT NULL GROUP BY u`)
	out := make(map[uuid.UUID]time.Time, len(rows))
	for _, row := range rows {
		out[row.U] = row.T
	}
	return out, err
}

type Project struct {
	ID        uuid.UUID `db:"id"`
	Title     string    `db:"title"`
	CreatedAt time.Time `db:"created_at"`
	HasData   bool      `db:"has_data"`
}

func (r *Reader) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := r.db.SelectContext(ctx, &out, `
		SELECT p.id, p.title, COALESCE(p.created_at, 'epoch') AS created_at,
			(EXISTS (SELECT 1 FROM test_cases c WHERE c.project_id = p.id AND c.deleted_at IS NULL)
			 OR EXISTS (SELECT 1 FROM test_issues i WHERE i.project_id = p.id AND i.deleted_at IS NULL)
			 OR EXISTS (SELECT 1 FROM test_runs t WHERE t.project_id = p.id AND t.deleted_at IS NULL)) AS has_data
		FROM projects p WHERE p.deleted_at IS NULL ORDER BY p.created_at, p.id`)
	return out, err
}

func (r *Reader) ProjectAssignees(ctx context.Context, projectID uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := r.db.SelectContext(ctx, &out, `
		SELECT DISTINCT contributor_id FROM project_assignees
		WHERE project_id = $1 AND deleted_at IS NULL AND contributor_id IS NOT NULL ORDER BY contributor_id`, projectID)
	return out, err
}

type Module struct {
	ID        uuid.UUID  `db:"id"`
	ParentID  *uuid.UUID `db:"parent_id"`
	Name      string     `db:"module_name"`
	SortOrder int        `db:"sort_order"`
	Archived  bool       `db:"is_archive"`
	DeletedAt *time.Time `db:"deleted_at"`
	CreatedAt time.Time  `db:"created_at"`
	PicID     *uuid.UUID `db:"pic_id"`
}

// picColumn is how to read table's pic_id: the column, prefixed with
// alias, when the dump has it — QA Tools started filling modules.pic_id
// and test_cases.pic_id on 2026-09-04, older dumps lack them — else NULL.
func (r *Reader) picColumn(ctx context.Context, table, alias string) (string, error) {
	var ok bool
	if err := r.db.GetContext(ctx, &ok, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'pic_id')`, table); err != nil {
		return "", err
	}
	if !ok {
		return "NULL::uuid", nil
	}
	return alias + "pic_id", nil
}

func (r *Reader) Modules(ctx context.Context, projectID uuid.UUID) ([]Module, error) {
	pic, err := r.picColumn(ctx, "modules", "")
	if err != nil {
		return nil, err
	}
	var out []Module
	err = r.db.SelectContext(ctx, &out, `
		SELECT id, parent_id, COALESCE(module_name, '') AS module_name, "order" AS sort_order, is_archive,
			deleted_at, COALESCE(created_at, updated_at, 'epoch') AS created_at, `+pic+` AS pic_id
		FROM modules WHERE project_id = $1 ORDER BY "order", id`, projectID)
	return out, err
}

type Case struct {
	ID           uuid.UUID  `db:"id"`
	ModuleID     *uuid.UUID `db:"module_id"`
	Code         string     `db:"case_code"`
	Title        string     `db:"title"`
	Precondition string     `db:"precondition"`
	Description  string     `db:"description"`
	Priority     string     `db:"priority"`
	Tag          string     `db:"tag"`
	Archived     bool       `db:"is_archive"`
	DeletedAt    *time.Time `db:"deleted_at"`
	CreatedBy    *uuid.UUID `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
	PicID        *uuid.UUID `db:"pic_id"`
}

// liveDetails is the set of run details the importer keeps: live details of
// live runs of this project, whose case belongs to the same project, one per
// (run, case) — the earliest. Every query that follows details reuses it.
const liveDetails = `
	SELECT DISTINCT ON (d.test_run_id, d.test_case_id)
		d.id, d.test_run_id, d.test_case_id, d.assignee_id, d.status, d.updated_status_at,
		COALESCE(d.created_at, 'epoch') AS created_at
	FROM test_run_details d
	JOIN test_runs t ON t.id = d.test_run_id
	JOIN test_cases c ON c.id = d.test_case_id
	WHERE t.project_id = $1 AND t.deleted_at IS NULL AND d.deleted_at IS NULL AND c.project_id = $1
	ORDER BY d.test_run_id, d.test_case_id, d.created_at, d.id`

func (r *Reader) Cases(ctx context.Context, projectID uuid.UUID) ([]Case, error) {
	pic, err := r.picColumn(ctx, "test_cases", "c.")
	if err != nil {
		return nil, err
	}
	var out []Case
	err = r.db.SelectContext(ctx, &out, `
		WITH kept AS (`+liveDetails+`)
		SELECT c.id, c.module_id, COALESCE(c.case_code, '') AS case_code, COALESCE(c.title, '') AS title,
			COALESCE(c.precondition, '') AS precondition, COALESCE(c.description, '') AS description,
			COALESCE(c.priority, '') AS priority, COALESCE(c.tag, '') AS tag, c.is_archive, c.deleted_at,
			c.created_by, COALESCE(c.created_at, 'epoch') AS created_at,
			COALESCE(c.updated_at, c.created_at, 'epoch') AS updated_at, `+pic+` AS pic_id
		FROM test_cases c
		WHERE c.project_id = $1
		  AND (c.deleted_at IS NULL OR c.id IN (SELECT test_case_id FROM kept))
		ORDER BY c.created_at, c.id`, projectID)
	return out, err
}

type Step struct {
	CaseID   uuid.UUID `db:"test_case_id"`
	Action   string    `db:"title"`
	Expected string    `db:"expected_result"`
}

func (r *Reader) Steps(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID][]Step, error) {
	var rows []Step
	err := r.db.SelectContext(ctx, &rows, `
		SELECT s.test_case_id, COALESCE(s.title, '') AS title, COALESCE(s.expected_result, '') AS expected_result
		FROM steps s JOIN test_cases c ON c.id = s.test_case_id
		WHERE c.project_id = $1 ORDER BY s.test_case_id, s.position_step, s.id`, projectID)
	out := map[uuid.UUID][]Step{}
	for _, s := range rows {
		out[s.CaseID] = append(out[s.CaseID], s)
	}
	return out, err
}

type TestPlan struct {
	ID        uuid.UUID  `db:"id"`
	Name      string     `db:"plan_name"`
	Start     *time.Time `db:"assignment_time"`
	End       *time.Time `db:"expiration_time"`
	Archived  bool       `db:"is_archive"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

func (r *Reader) TestPlans(ctx context.Context, projectID uuid.UUID) ([]TestPlan, error) {
	var out []TestPlan
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, COALESCE(plan_name, '') AS plan_name, assignment_time, expiration_time, is_archive, created_by,
			COALESCE(created_at, 'epoch') AS created_at
		FROM test_plans WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, projectID)
	return out, err
}

type Release struct {
	ID        uuid.UUID  `db:"id"`
	Name      string     `db:"name"`
	Start     *time.Time `db:"start_date"`
	End       *time.Time `db:"end_date"`
	Archived  bool       `db:"is_archive"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

func (r *Reader) Releases(ctx context.Context, projectID uuid.UUID) ([]Release, error) {
	var out []Release
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, COALESCE(name, '') AS name, start_date, end_date, is_archive, created_by,
			COALESCE(created_at, 'epoch') AS created_at
		FROM releases WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, projectID)
	return out, err
}

type Run struct {
	ID          uuid.UUID  `db:"id"`
	Title       string     `db:"title"`
	Description string     `db:"description"`
	TestPlanID  *uuid.UUID `db:"test_plan_id"`
	ReleaseID   *uuid.UUID `db:"release_id"`
	Version     string     `db:"version"`
	Browser     string     `db:"browser"`
	Archived    bool       `db:"is_archive"`
	AssigneeID  *uuid.UUID `db:"assignee_id"`
	CreatedBy   *uuid.UUID `db:"created_by"`
	CreatedAt   time.Time  `db:"created_at"`
}

func (r *Reader) Runs(ctx context.Context, projectID uuid.UUID) ([]Run, error) {
	var out []Run
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, COALESCE(title, '') AS title, COALESCE(description, '') AS description, test_plan_id, release_id,
			COALESCE(version, '') AS version, COALESCE(browser, '') AS browser, is_archive, assignee_id, created_by,
			COALESCE(created_at, 'epoch') AS created_at
		FROM test_runs WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, projectID)
	return out, err
}

type Detail struct {
	ID         uuid.UUID  `db:"id"`
	RunID      uuid.UUID  `db:"test_run_id"`
	CaseID     uuid.UUID  `db:"test_case_id"`
	AssigneeID *uuid.UUID `db:"assignee_id"`
	Status     string     `db:"status"`
	StatusAt   *time.Time `db:"updated_status_at"`
	CreatedAt  time.Time  `db:"created_at"`
}

func (r *Reader) Details(ctx context.Context, projectID uuid.UUID) ([]Detail, error) {
	var out []Detail
	err := r.db.SelectContext(ctx, &out,
		`SELECT * FROM (`+liveDetails+`) kept ORDER BY test_run_id, created_at, id`, projectID)
	return out, err
}

type Result struct {
	ID        uuid.UUID  `db:"id"`
	DetailID  uuid.UUID  `db:"test_run_detail_id"`
	Status    string     `db:"status"`
	Comment   string     `db:"comment_test"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

func (r *Reader) Results(ctx context.Context, projectID uuid.UUID) ([]Result, error) {
	var out []Result
	err := r.db.SelectContext(ctx, &out, `
		WITH kept AS (`+liveDetails+`)
		SELECT r.id, r.test_run_detail_id, COALESCE(r.status, '') AS status, COALESCE(r.comment_test, '') AS comment_test,
			r.created_by, COALESCE(r.created_at, 'epoch') AS created_at
		FROM results r WHERE r.deleted_at IS NULL AND r.test_run_detail_id IN (SELECT id FROM kept)
		ORDER BY r.created_at, r.id`, projectID)
	return out, err
}

type Issue struct {
	ID          uuid.UUID  `db:"id"`
	Code        string     `db:"code_issue"`
	Title       string     `db:"title"`
	Description string     `db:"description"`
	Steps       string     `db:"step_issue"`
	Environment string     `db:"environment_issue"`
	ReleaseID   *uuid.UUID `db:"release_id"`
	ModuleID    *uuid.UUID `db:"module_id"`
	AssigneeID  *uuid.UUID `db:"assignee_issue"`
	CreatedBy   *uuid.UUID `db:"created_by"`
	Priority    string     `db:"priority"`
	Severity    string     `db:"severity"`
	Status      string     `db:"status"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
}

func (r *Reader) Issues(ctx context.Context, projectID uuid.UUID) ([]Issue, error) {
	var out []Issue
	err := r.db.SelectContext(ctx, &out, `
		SELECT id, COALESCE(code_issue, '') AS code_issue, COALESCE(title, '') AS title,
			COALESCE(description, '') AS description, COALESCE(step_issue, '') AS step_issue,
			COALESCE(environment_issue, '') AS environment_issue, release_id, module_id, assignee_issue, created_by,
			COALESCE(priority, '') AS priority, COALESCE(severity, '') AS severity, COALESCE(status, '') AS status,
			COALESCE(created_at, 'epoch') AS created_at, COALESCE(updated_at, created_at, 'epoch') AS updated_at
		FROM test_issues WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, projectID)
	return out, err
}

type IssueLink struct {
	DetailID  uuid.UUID `db:"test_run_detail_id"`
	IssueID   uuid.UUID `db:"test_issue_id"`
	CreatedAt time.Time `db:"created_at"`
}

func (r *Reader) IssueLinks(ctx context.Context, projectID uuid.UUID) ([]IssueLink, error) {
	var out []IssueLink
	err := r.db.SelectContext(ctx, &out, `
		SELECT DISTINCT ON (l.test_run_detail_id, l.test_issue_id) l.test_run_detail_id, l.test_issue_id,
			COALESCE(l.created_at, 'epoch') AS created_at
		FROM test_run_detail_issues l JOIN test_issues i ON i.id = l.test_issue_id
		WHERE i.project_id = $1 AND i.deleted_at IS NULL AND l.deleted_at IS NULL
		ORDER BY l.test_run_detail_id, l.test_issue_id, l.created_at`, projectID)
	return out, err
}

type Comment struct {
	ID        uuid.UUID  `db:"id"`
	IssueID   uuid.UUID  `db:"test_issue_id"`
	Body      string     `db:"comment_summary"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

func (r *Reader) Comments(ctx context.Context, projectID uuid.UUID) ([]Comment, error) {
	var out []Comment
	err := r.db.SelectContext(ctx, &out, `
		SELECT c.id, c.test_issue_id, COALESCE(c.comment_summary, '') AS comment_summary, c.created_by,
			COALESCE(c.created_at, 'epoch') AS created_at
		FROM comments c JOIN test_issues i ON i.id = c.test_issue_id
		WHERE i.project_id = $1 AND i.deleted_at IS NULL AND c.deleted_at IS NULL
		ORDER BY c.created_at, c.id`, projectID)
	return out, err
}

type IssueLog struct {
	ID        uuid.UUID  `db:"id"`
	IssueID   uuid.UUID  `db:"test_issue_id"`
	From      string     `db:"status_before"`
	To        string     `db:"status_after"`
	By        *uuid.UUID `db:"updated_by"`
	CreatedAt time.Time  `db:"created_at"`
}

func (r *Reader) IssueLogs(ctx context.Context, projectID uuid.UUID) ([]IssueLog, error) {
	var out []IssueLog
	err := r.db.SelectContext(ctx, &out, `
		SELECT l.uuid AS id, l.test_issue_id, COALESCE(l.status_before, '') AS status_before,
			COALESCE(l.status_after, '') AS status_after, l.updated_by, COALESCE(l.created_at, 'epoch') AS created_at
		FROM test_issue_logs l JOIN test_issues i ON i.id = l.test_issue_id
		WHERE i.project_id = $1 AND i.deleted_at IS NULL
		ORDER BY l.created_at, l.uuid`, projectID)
	return out, err
}

type File struct {
	ID         uuid.UUID `db:"id"`
	Name       string    `db:"name"`
	Path       string    `db:"path"`
	Size       int64     `db:"size"`
	OwnerID    uuid.UUID `db:"relation_id"`
	OwnerModel string    `db:"relation_model"`
	CreatedAt  time.Time `db:"created_at"`
}

// Files returns attachments whose owner is a result, an issue or a comment of
// this project. Release and user attachments are not migrated (spec §7).
func (r *Reader) Files(ctx context.Context, projectID uuid.UUID) ([]File, error) {
	var out []File
	err := r.db.SelectContext(ctx, &out, `
		SELECT m.id, COALESCE(m.name, '') AS name, COALESCE(m.path, '') AS path,
			COALESCE(m.size, 0)::bigint AS size, m.relation_id, m.relation_model,
			COALESCE(m.created_at, 'epoch') AS created_at
		FROM minio m
		WHERE (m.relation_model = 'App\Models\Result' AND m.relation_id IN (
				SELECT r.id FROM results r JOIN test_run_details d ON d.id = r.test_run_detail_id
				JOIN test_runs t ON t.id = d.test_run_id WHERE t.project_id = $1))
		   OR (m.relation_model = 'App\Models\TestIssue' AND m.relation_id IN (
				SELECT id FROM test_issues WHERE project_id = $1))
		   OR (m.relation_model = 'App\Models\Comment' AND m.relation_id IN (
				SELECT c.id FROM comments c JOIN test_issues i ON i.id = c.test_issue_id WHERE i.project_id = $1))
		ORDER BY m.created_at, m.id`, projectID)
	return out, err
}
