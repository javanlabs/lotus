package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// QAWorkloadCounts is a breakdown of run items by status.
type QAWorkloadCounts struct {
	Total    int `db:"total"`
	Untested int `db:"untested"`
	Passed   int `db:"passed"`
	Failed   int `db:"failed"`
	Blocked  int `db:"blocked"`
	Skipped  int `db:"skipped"`
}

func (c *QAWorkloadCounts) add(o QAWorkloadCounts) {
	c.Total += o.Total
	c.Untested += o.Untested
	c.Passed += o.Passed
	c.Failed += o.Failed
	c.Blocked += o.Blocked
	c.Skipped += o.Skipped
}

// QAWorkloadPerson is one member's QA work in a month: the projects they
// had run items in, the cases they wrote, their run items by status and
// the issues they reported. Items count by the run's month, not by when
// they were executed — a run belongs to the month it was created in.
type QAWorkloadPerson struct {
	UserID         uuid.UUID `db:"user_id"`
	Name           string    `db:"name"`
	Role           string    `db:"role"`
	Projects       int       `db:"projects"`
	CasesCreated   int       `db:"cases_created"`
	IssuesReported int       `db:"issues_reported"`
	QAWorkloadCounts
}

// QAWorkloadProject is one project's QA activity in a month.
type QAWorkloadProject struct {
	ProjectID     uuid.UUID `db:"project_id"`
	ProjectSlug   string    `db:"project_slug"`
	Name          string    `db:"name"`
	CasesCreated  int       `db:"cases_created"`
	RunsCreated   int       `db:"runs_created"`
	IssuesCreated int       `db:"issues_created"`
	QAWorkloadCounts
}

// QAWorkloadRun is a run created in the month, with its items by status —
// all of them (AssigneeID nil, in QAWorkload.Runs), or one person's
// (in QAWorkload.PersonRuns).
type QAWorkloadRun struct {
	RunID       uuid.UUID  `db:"run_id"`
	ProjectID   uuid.UUID  `db:"project_id"`
	ProjectName string     `db:"project_name"`
	ProjectSlug string     `db:"project_slug"`
	Name        string     `db:"name"`
	AssigneeID  *uuid.UUID `db:"assignee_id"`
	CreatedAt   time.Time  `db:"created_at"`
	QAWorkloadCounts
}

// QAWorkloadIssue is an issue reported in the month.
type QAWorkloadIssue struct {
	IssueID     uuid.UUID  `db:"issue_id"`
	ProjectID   uuid.UUID  `db:"project_id"`
	ProjectName string     `db:"project_name"`
	ProjectSlug string     `db:"project_slug"`
	Key         int        `db:"key"`
	Title       string     `db:"title"`
	Status      string     `db:"status"`
	Severity    string     `db:"severity"`
	ReportedBy  *uuid.UUID `db:"reported_by"`
	Reporter    string     `db:"reporter"`
	CreatedAt   time.Time  `db:"created_at"`
}

// QAWorkload is the organization's QA recap for one month (/qa/workload).
type QAWorkload struct {
	Month      string
	People     []QAWorkloadPerson
	Projects   []QAWorkloadProject
	Runs       []QAWorkloadRun
	PersonRuns []QAWorkloadRun
	Issues     []QAWorkloadIssue
}

// QAWorkloadRoles are the roles the people recap lists: the team doing QA.
// Guests (clients) and viewers are left out; so are suspended members.
var QAWorkloadRoles = []string{"owner", "admin", "member"}

// CurrentQAMonth is this month as YYYY-MM on the database's clock — the
// same clock LoadQAWorkload cuts months with.
func CurrentQAMonth(ctx context.Context, s tenant.Scope) (string, error) {
	var out string
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT to_char(now(), 'YYYY-MM')`)
	})
	return out, err
}

// workloadMonth is the CTE every workload query starts from: the month's
// first instant and the next month's, in the database's timezone.
const workloadMonth = `b AS (SELECT $1::date::timestamptz AS lo, ($1::date + interval '1 month')::timestamptz AS hi)`

// workloadRuns is the month's runs with their project.
const workloadRuns = `mr AS (
	SELECT r.id, r.name, r.created_at, pl.project_id
	FROM qa_runs r JOIN qa_plans pl ON pl.id = r.plan_id, b
	WHERE r.created_at >= b.lo AND r.created_at < b.hi)`

const workloadCounts = `count(it.id) AS total,
	count(it.id) FILTER (WHERE it.status = 'untested') AS untested,
	count(it.id) FILTER (WHERE it.status = 'passed') AS passed,
	count(it.id) FILTER (WHERE it.status = 'failed') AS failed,
	count(it.id) FILTER (WHERE it.status = 'blocked') AS blocked,
	count(it.id) FILTER (WHERE it.status = 'skipped') AS skipped`

// LoadQAWorkload is the recap for month (YYYY-MM): people, projects with
// any activity, the month's runs and the issues reported in it. A month is
// cut on the database's clock. A malformed month is ErrInvalidInput.
func LoadQAWorkload(ctx context.Context, s tenant.Scope, month string) (*QAWorkload, error) {
	if t, err := time.Parse("2006-01", month); err != nil || t.Format("2006-01") != month {
		return nil, ErrInvalidInput
	}
	first := month + "-01"
	out := &QAWorkload{Month: month}
	var runs []QAWorkloadRun
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &out.People, `
			WITH `+workloadMonth+`, `+workloadRuns+`,
			items AS (
				SELECT it.assignee_id AS user_id, count(DISTINCT mr.project_id) AS projects, `+workloadCounts+`
				FROM qa_run_items it JOIN mr ON mr.id = it.run_id
				WHERE it.assignee_id IS NOT NULL GROUP BY it.assignee_id),
			cases AS (
				SELECT c.created_by AS user_id, count(*) AS n FROM qa_cases c, b
				WHERE c.created_by IS NOT NULL AND c.created_at >= b.lo AND c.created_at < b.hi GROUP BY c.created_by),
			issues AS (
				SELECT i.reported_by AS user_id, count(*) AS n FROM qa_issues i, b
				WHERE i.reported_by IS NOT NULL AND i.created_at >= b.lo AND i.created_at < b.hi GROUP BY i.reported_by)
			SELECT m.user_id, COALESCE(NULLIF(btrim(u.name), ''), u.email) AS name, r.key AS role,
				COALESCE(items.projects, 0) AS projects,
				COALESCE(cases.n, 0) AS cases_created,
				COALESCE(issues.n, 0) AS issues_reported,
				COALESCE(items.total, 0) AS total, COALESCE(items.untested, 0) AS untested,
				COALESCE(items.passed, 0) AS passed, COALESCE(items.failed, 0) AS failed,
				COALESCE(items.blocked, 0) AS blocked, COALESCE(items.skipped, 0) AS skipped
			FROM memberships m
			JOIN users u ON u.id = m.user_id
			JOIN roles r ON r.id = m.role_id
			LEFT JOIN items ON items.user_id = m.user_id
			LEFT JOIN cases ON cases.user_id = m.user_id
			LEFT JOIN issues ON issues.user_id = m.user_id
			WHERE m.org_id = $2 AND m.status = 'active' AND r.key = ANY($3)
			ORDER BY lower(COALESCE(NULLIF(btrim(u.name), ''), u.email)), m.user_id`,
			first, s.OrgID(), QAWorkloadRoles); err != nil {
			return err
		}
		if err := tx.SelectContext(ctx, &out.Projects, `
			WITH `+workloadMonth+`, `+workloadRuns+`,
			items AS (
				SELECT mr.project_id, count(DISTINCT mr.id) AS runs, `+workloadCounts+`
				FROM mr LEFT JOIN qa_run_items it ON it.run_id = mr.id GROUP BY mr.project_id),
			cases AS (
				SELECT c.project_id, count(*) AS n FROM qa_cases c, b
				WHERE c.created_at >= b.lo AND c.created_at < b.hi GROUP BY c.project_id),
			issues AS (
				SELECT i.project_id, count(*) AS n FROM qa_issues i, b
				WHERE i.created_at >= b.lo AND i.created_at < b.hi GROUP BY i.project_id)
			SELECT p.id AS project_id, p.slug AS project_slug, p.name,
				COALESCE(cases.n, 0) AS cases_created,
				COALESCE(items.runs, 0) AS runs_created,
				COALESCE(issues.n, 0) AS issues_created,
				COALESCE(items.total, 0) AS total, COALESCE(items.untested, 0) AS untested,
				COALESCE(items.passed, 0) AS passed, COALESCE(items.failed, 0) AS failed,
				COALESCE(items.blocked, 0) AS blocked, COALESCE(items.skipped, 0) AS skipped
			FROM projects p
			LEFT JOIN items ON items.project_id = p.id
			LEFT JOIN cases ON cases.project_id = p.id
			LEFT JOIN issues ON issues.project_id = p.id
			WHERE p.deleted_at IS NULL AND (items.project_id IS NOT NULL OR cases.project_id IS NOT NULL OR issues.project_id IS NOT NULL)
			ORDER BY lower(p.name), p.id`, first); err != nil {
			return err
		}
		if err := tx.SelectContext(ctx, &runs, `
			WITH `+workloadMonth+`, `+workloadRuns+`
			SELECT mr.id AS run_id, mr.project_id, p.name AS project_name, p.slug AS project_slug, mr.name, mr.created_at,
				it.assignee_id, `+workloadCounts+`
			FROM mr JOIN projects p ON p.id = mr.project_id AND p.deleted_at IS NULL
			LEFT JOIN qa_run_items it ON it.run_id = mr.id
			GROUP BY mr.id, mr.project_id, p.name, p.slug, mr.name, mr.created_at, it.assignee_id
			ORDER BY mr.created_at, mr.id`, first); err != nil {
			return err
		}
		return tx.SelectContext(ctx, &out.Issues, `
			WITH `+workloadMonth+`
			SELECT i.id AS issue_id, i.project_id, p.name AS project_name, p.slug AS project_slug, i.key, i.title, i.status, i.severity,
				i.reported_by, COALESCE(NULLIF(btrim(u.name), ''), u.email, '') AS reporter, i.created_at
			FROM qa_issues i JOIN projects p ON p.id = i.project_id AND p.deleted_at IS NULL
			LEFT JOIN users u ON u.id = i.reported_by, b
			WHERE i.created_at >= b.lo AND i.created_at < b.hi
			ORDER BY i.created_at, i.id`, first)
	})
	if err != nil {
		return nil, err
	}
	// One row per (run, assignee): the run's totals are the sum, and the
	// assigned rows are each person's share.
	index := map[uuid.UUID]int{}
	for _, r := range runs {
		i, ok := index[r.RunID]
		if !ok {
			i = len(out.Runs)
			index[r.RunID] = i
			total := r
			total.AssigneeID = nil
			total.QAWorkloadCounts = QAWorkloadCounts{}
			out.Runs = append(out.Runs, total)
		}
		out.Runs[i].add(r.QAWorkloadCounts)
		if r.AssigneeID != nil {
			out.PersonRuns = append(out.PersonRuns, r)
		}
	}
	return out, nil
}
