package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// QAImportWriter writes legacy QA rows inside one transaction with the ids
// and timestamps the importer decides — the ordinary Create* functions
// allocate keys and stamp now(), which an import must not do.
type QAImportWriter struct {
	ctx   context.Context
	tx    *sqlx.Tx
	orgID uuid.UUID
}

func WithQAImportTx(ctx context.Context, s tenant.Scope, fn func(w *QAImportWriter) error) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		return fn(&QAImportWriter{ctx: ctx, tx: tx, orgID: s.OrgID()})
	})
}

// LockLegacy serialises work on one legacy row (a project) across
// concurrent import runs until this transaction ends. Call it before
// checking the project's done marker, so a second run waits for the first
// and then sees the marker instead of importing the project twice.
func (w *QAImportWriter) LockLegacy(table string, legacyID uuid.UUID) error {
	_, err := w.tx.ExecContext(w.ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"qa_import:"+w.orgID.String()+":"+table+":"+legacyID.String())
	return err
}

func (w *QAImportWriter) Lookup(table string, legacyID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := w.tx.GetContext(w.ctx, &id,
		`SELECT new_id FROM qa_legacy_ids WHERE legacy_table = $1 AND legacy_id = $2`, table, legacyID)
	if isNoRows(err) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

func (w *QAImportWriter) Remember(table string, legacyID, newID uuid.UUID) error {
	_, err := w.tx.ExecContext(w.ctx, `
		INSERT INTO qa_legacy_ids (org_id, legacy_table, legacy_id, new_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, legacy_table, legacy_id) DO UPDATE SET new_id = EXCLUDED.new_id`,
		w.orgID, table, legacyID, newID)
	return err
}

// EnsureProject returns the live project with this slug when reuse is true
// (a slug an operator mapped on purpose), and otherwise always creates a new
// project, appending -2, -3, … on a clash — two legacy projects whose titles
// slugify alike must not be merged into one.
func (w *QAImportWriter) EnsureProject(slug, name string, folderID *uuid.UUID, reuse bool) (uuid.UUID, error) {
	var id uuid.UUID
	if reuse {
		err := w.tx.GetContext(w.ctx, &id, `SELECT id FROM projects WHERE slug = $1 AND archived_at IS NULL`, slug)
		if err == nil {
			return id, nil
		}
		if !isNoRows(err) {
			return uuid.Nil, err
		}
	}
	var err error
	for attempt := 1; attempt <= 50; attempt++ {
		candidate := slug
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", slug, attempt)
		}
		if _, err := w.tx.ExecContext(w.ctx, `SAVEPOINT ensure_project`); err != nil {
			return uuid.Nil, err
		}
		err = w.tx.GetContext(w.ctx, &id, `
			INSERT INTO projects (org_id, slug, name, folder_id) VALUES ($1, $2, $3, $4) RETURNING id`,
			w.orgID, candidate, name, folderID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if _, err := w.tx.ExecContext(w.ctx, `ROLLBACK TO SAVEPOINT ensure_project`); err != nil {
				return uuid.Nil, err
			}
			continue
		}
		return id, err
	}
	return uuid.Nil, fmt.Errorf("no free slug for %q", slug)
}

func (w *QAImportWriter) AddProjectMember(projectID, userID uuid.UUID) error {
	_, err := w.tx.ExecContext(w.ctx, `
		INSERT INTO project_members (org_id, project_id, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, user_id) DO NOTHING`, w.orgID, projectID, userID)
	return err
}

func (w *QAImportWriter) insert(query string, args ...any) (uuid.UUID, error) {
	var id uuid.UUID
	err := w.tx.GetContext(w.ctx, &id, query, args...)
	return id, err
}

func (w *QAImportWriter) InsertSection(v QASection) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_sections (org_id, project_id, parent_id, name, sort_order, archived_at, created_at, pic_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		w.orgID, v.ProjectID, v.ParentID, v.Name, v.SortOrder, v.ArchivedAt, v.CreatedAt, v.PicID)
}

func (w *QAImportWriter) InsertCase(v QACase) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_cases (org_id, project_id, section_id, key, title, precondition, notes, steps, priority,
			negative, version, archived_at, created_by, created_at, updated_at, pic_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		w.orgID, v.ProjectID, v.SectionID, v.Key, v.Title, v.Precondition, v.Notes, v.Steps, v.Priority,
		v.Negative, v.Version, v.ArchivedAt, v.CreatedBy, v.CreatedAt, v.UpdatedAt, v.PicID)
}

func (w *QAImportWriter) InsertPlan(v QAPlan) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_plans (org_id, project_id, name, phase, environment, build, starts_on, ends_on, closed_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		w.orgID, v.ProjectID, v.Name, v.Phase, v.Environment, v.Build, v.StartsOn, v.EndsOn, v.ClosedAt, v.CreatedBy, v.CreatedAt)
}

func (w *QAImportWriter) InsertRun(v QARun) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_runs (org_id, plan_id, name, cycle, retest_of, notes, closed_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		w.orgID, v.PlanID, v.Name, v.Cycle, v.RetestOf, v.Notes, v.ClosedAt, v.CreatedBy, v.CreatedAt)
}

func (w *QAImportWriter) InsertRunItem(v QARunItem) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_run_items (org_id, run_id, case_id, group_name, position, title, precondition, steps,
			case_version, assignee_id, status, status_at, executed_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		w.orgID, v.RunID, v.CaseID, v.GroupName, v.Position, v.Title, v.Precondition, v.Steps,
		v.CaseVersion, v.AssigneeID, v.Status, v.StatusAt, v.ExecutedBy)
}

func (w *QAImportWriter) InsertResult(v QAResult) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_results (org_id, item_id, status, comment, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		w.orgID, v.ItemID, v.Status, v.Comment, v.CreatedBy, v.CreatedAt)
}

func (w *QAImportWriter) InsertIssue(v QAIssue) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_issues (org_id, project_id, key, title, description, steps_to_reproduce, severity, status,
			environment, plan_id, section_id, assignee_id, reported_by, source, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		w.orgID, v.ProjectID, v.Key, v.Title, v.Description, v.StepsToReproduce, v.Severity, v.Status,
		v.Environment, v.PlanID, v.SectionID, v.AssigneeID, v.ReportedBy, v.Source, v.CreatedAt, v.UpdatedAt)
}

func (w *QAImportWriter) InsertIssueLink(itemID, issueID uuid.UUID, createdAt time.Time) error {
	_, err := w.tx.ExecContext(w.ctx, `
		INSERT INTO qa_issue_links (org_id, item_id, issue_id, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, w.orgID, itemID, issueID, createdAt)
	return err
}

func (w *QAImportWriter) InsertIssueEvent(v QAIssueEvent) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_issue_events (org_id, issue_id, kind, body, status_from, status_to, actor_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		w.orgID, v.IssueID, v.Kind, v.Body, v.StatusFrom, v.StatusTo, v.ActorID, v.CreatedAt)
}

func (w *QAImportWriter) InsertAttachment(v QAAttachment) (uuid.UUID, error) {
	return w.insert(`
		INSERT INTO qa_attachments (org_id, result_id, issue_id, event_id, file_name, content_type, size_bytes,
			storage_key, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		w.orgID, v.ResultID, v.IssueID, v.EventID, v.FileName, v.ContentType, v.SizeBytes,
		v.StorageKey, v.CreatedBy, v.CreatedAt)
}

func (w *QAImportWriter) ClosePlan(planID uuid.UUID, at time.Time) error {
	if _, err := w.tx.ExecContext(w.ctx, `UPDATE qa_plans SET closed_at = $2 WHERE id = $1`, planID, at); err != nil {
		return err
	}
	_, err := w.tx.ExecContext(w.ctx, `UPDATE qa_runs SET closed_at = COALESCE(closed_at, $2) WHERE plan_id = $1`, planID, at)
	return err
}

func ListQALegacyIDs(ctx context.Context, s tenant.Scope, table string) (map[uuid.UUID]uuid.UUID, error) {
	var rows []struct {
		Legacy uuid.UUID `db:"legacy_id"`
		New    uuid.UUID `db:"new_id"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `SELECT legacy_id, new_id FROM qa_legacy_ids WHERE legacy_table = $1`, table)
	})
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, r := range rows {
		out[r.Legacy] = r.New
	}
	return out, err
}

type QACounts struct {
	Cases, ArchivedCases, Runs, Items, Results, Issues, IssueEvents int
	ItemStatus, IssueStatus                                         map[string]int
}

func QACountsForProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID) (QACounts, error) {
	out := QACounts{ItemStatus: map[string]int{}, IssueStatus: map[string]int{}}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out.Cases, `SELECT count(*) FROM qa_cases WHERE project_id = $1`, projectID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out.ArchivedCases, `SELECT count(*) FROM qa_cases WHERE project_id = $1 AND archived_at IS NOT NULL`, projectID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out.Runs, `SELECT count(*) FROM qa_runs r JOIN qa_plans p ON p.id = r.plan_id WHERE p.project_id = $1`, projectID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out.Results, `
			SELECT count(*) FROM qa_results x JOIN qa_run_items i ON i.id = x.item_id
			JOIN qa_runs r ON r.id = i.run_id JOIN qa_plans p ON p.id = r.plan_id WHERE p.project_id = $1`, projectID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out.IssueEvents, `
			SELECT count(*) FROM qa_issue_events e JOIN qa_issues i ON i.id = e.issue_id WHERE i.project_id = $1`, projectID); err != nil {
			return err
		}
		var rows []struct {
			K string `db:"k"`
			N int    `db:"n"`
		}
		if err := tx.SelectContext(ctx, &rows, `
			SELECT i.status AS k, count(*) AS n FROM qa_run_items i JOIN qa_runs r ON r.id = i.run_id
			JOIN qa_plans p ON p.id = r.plan_id WHERE p.project_id = $1 GROUP BY i.status`, projectID); err != nil {
			return err
		}
		for _, r := range rows {
			out.ItemStatus[r.K] = r.N
			out.Items += r.N
		}
		rows = nil
		if err := tx.SelectContext(ctx, &rows, `
			SELECT status AS k, count(*) AS n FROM qa_issues WHERE project_id = $1 GROUP BY status`, projectID); err != nil {
			return err
		}
		for _, r := range rows {
			out.IssueStatus[r.K] = r.N
			out.Issues += r.N
		}
		return nil
	})
	return out, err
}

// CountProjectsWithQAData counts this organization's projects holding any
// case, plan or issue. Verify compares it with the imported projects, so a
// duplicated or stray import shows up.
func CountProjectsWithQAData(ctx context.Context, s tenant.Scope) (int, error) {
	var n int
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, `
			SELECT count(*) FROM (
				SELECT project_id FROM qa_cases
				UNION SELECT project_id FROM qa_plans
				UNION SELECT project_id FROM qa_issues
			) p`)
	})
	return n, err
}
