package repo

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

var (
	QAIssueSeverities = []string{"blocker", "critical", "major", "minor", "trivial"}
	QAIssueStatuses   = []string{"new", "open", "fixed", "closed", "invalid", "duplicate", "reopened"}
)

type QAIssue struct {
	ID               uuid.UUID  `db:"id"`
	OrgID            uuid.UUID  `db:"org_id"`
	ProjectID        uuid.UUID  `db:"project_id"`
	Key              int        `db:"key"`
	Title            string     `db:"title"`
	Description      string     `db:"description"`
	StepsToReproduce string     `db:"steps_to_reproduce"`
	Severity         string     `db:"severity"`
	Status           string     `db:"status"`
	Environment      string     `db:"environment"`
	PlanID           *uuid.UUID `db:"plan_id"`
	SectionID        *uuid.UUID `db:"section_id"`
	AssigneeID       *uuid.UUID `db:"assignee_id"`
	ReportedBy       *uuid.UUID `db:"reported_by"`
	Source           string     `db:"source"`
	CreatedAt        time.Time  `db:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at"`
}

const qaIssueCols = `id, org_id, project_id, key, title, description, steps_to_reproduce, severity, status,
	environment, plan_id, section_id, assignee_id, reported_by, source, created_at, updated_at`

type QAIssueInput struct {
	Title            string
	Description      string
	StepsToReproduce string
	Severity         string
	Environment      string
	PlanID           *uuid.UUID
	SectionID        *uuid.UUID
	AssigneeID       *uuid.UUID
	Source           string
}

func (in QAIssueInput) normalized() (QAIssueInput, error) {
	t, err := normalizeQAName(in.Title, MaxQATitleLength)
	if err != nil {
		return in, err
	}
	if !slices.Contains(QAIssueSeverities, in.Severity) {
		return in, ErrInvalidInput
	}
	if in.Source == "" {
		in.Source = "internal"
	}
	if in.Source != "internal" && in.Source != "client" {
		return in, ErrInvalidInput
	}
	in.Title = t
	in.Description = strings.TrimSpace(in.Description)
	in.StepsToReproduce = strings.TrimSpace(in.StepsToReproduce)
	in.Environment = strings.TrimSpace(in.Environment)
	return in, nil
}

// itemPlanInProject returns the plan of itemID, or ErrInvalidInput when the
// item belongs to another project.
func itemPlanInProject(ctx context.Context, tx *sqlx.Tx, itemID, projectID uuid.UUID) (uuid.UUID, error) {
	var row struct {
		PlanID    uuid.UUID `db:"plan_id"`
		ProjectID uuid.UUID `db:"project_id"`
	}
	if err := tx.GetContext(ctx, &row, `
		SELECT p.id AS plan_id, p.project_id FROM qa_run_items i
		JOIN qa_runs r ON r.id = i.run_id JOIN qa_plans p ON p.id = r.plan_id
		WHERE i.id = $1`, itemID); err != nil {
		return uuid.Nil, err
	}
	if row.ProjectID != projectID {
		return uuid.Nil, ErrInvalidInput
	}
	return row.PlanID, nil
}

func CreateQAIssue(ctx context.Context, s tenant.Scope, projectID uuid.UUID, in QAIssueInput, reportedBy *uuid.UUID, fromItem *uuid.UUID) (*QAIssue, error) {
	in, err := in.normalized()
	if err != nil {
		return nil, err
	}
	var out QAIssue
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if fromItem != nil {
			planID, err := itemPlanInProject(ctx, tx, *fromItem, projectID)
			if err != nil {
				return err
			}
			if in.PlanID == nil {
				in.PlanID = &planID
			}
		}
		key, err := nextQAKey(ctx, tx, "qa_issues", projectID)
		if err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out, `
			INSERT INTO qa_issues (org_id, project_id, key, title, description, steps_to_reproduce, severity,
				environment, plan_id, section_id, assignee_id, reported_by, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING `+qaIssueCols,
			s.OrgID(), projectID, key, in.Title, in.Description, in.StepsToReproduce, in.Severity,
			in.Environment, in.PlanID, in.SectionID, in.AssigneeID, reportedBy, in.Source); err != nil {
			return err
		}
		if fromItem != nil {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO qa_issue_links (org_id, item_id, issue_id) VALUES ($1, $2, $3)`, s.OrgID(), *fromItem, out.ID)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQAIssue(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QAIssue, error) {
	var out QAIssue
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaIssueCols+` FROM qa_issues WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQAIssueByKey(ctx context.Context, s tenant.Scope, projectID uuid.UUID, key int) (*QAIssue, error) {
	var out QAIssue
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaIssueCols+` FROM qa_issues WHERE project_id = $1 AND key = $2`, projectID, key)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

type QAIssueFilter struct {
	Statuses   []string
	Severity   string
	Source     string
	PlanID     *uuid.UUID
	AssigneeID *uuid.UUID
}

func ListQAIssues(ctx context.Context, s tenant.Scope, projectID uuid.UUID, f QAIssueFilter) ([]QAIssue, error) {
	var statuses any
	if len(f.Statuses) > 0 {
		statuses = f.Statuses
	}
	var out []QAIssue
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaIssueCols+` FROM qa_issues
			WHERE project_id = $1
			  AND ($2::text[] IS NULL OR status = ANY($2))
			  AND ($3 = '' OR severity = $3)
			  AND ($4 = '' OR source = $4)
			  AND ($5::uuid IS NULL OR plan_id = $5)
			  AND ($6::uuid IS NULL OR assignee_id = $6)
			ORDER BY created_at DESC, key DESC`, projectID, statuses, f.Severity, f.Source, f.PlanID, f.AssigneeID)
	})
	return out, err
}

func UpdateQAIssue(ctx context.Context, s tenant.Scope, id uuid.UUID, in QAIssueInput) error {
	in, err := in.normalized()
	if err != nil {
		return err
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_issues SET title = $2, description = $3, steps_to_reproduce = $4, severity = $5,
				environment = $6, plan_id = $7, section_id = $8, assignee_id = $9, updated_at = now()
			WHERE id = $1`, id, in.Title, in.Description, in.StepsToReproduce, in.Severity,
			in.Environment, in.PlanID, in.SectionID, in.AssigneeID)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

type QAIssueEvent struct {
	ID         uuid.UUID  `db:"id"`
	OrgID      uuid.UUID  `db:"org_id"`
	IssueID    uuid.UUID  `db:"issue_id"`
	Kind       string     `db:"kind"`
	Body       string     `db:"body"`
	StatusFrom *string    `db:"status_from"`
	StatusTo   *string    `db:"status_to"`
	ActorID    *uuid.UUID `db:"actor_id"`
	CreatedAt  time.Time  `db:"created_at"`
}

const qaEventCols = `id, org_id, issue_id, kind, body, status_from, status_to, actor_id, created_at`

func ChangeQAIssueStatus(ctx context.Context, s tenant.Scope, id uuid.UUID, to string, actor *uuid.UUID, note string) error {
	if !slices.Contains(QAIssueStatuses, to) {
		return ErrInvalidInput
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var from string
		if err := tx.GetContext(ctx, &from, `SELECT status FROM qa_issues WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		if from == to {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE qa_issues SET status = $2, updated_at = now() WHERE id = $1`, id, to); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO qa_issue_events (org_id, issue_id, kind, body, status_from, status_to, actor_id)
			VALUES ($1, $2, 'status', $3, $4, $5, $6)`, s.OrgID(), id, strings.TrimSpace(note), from, to, actor)
		return err
	})
	return qaWriteErr(err)
}

func CommentQAIssue(ctx context.Context, s tenant.Scope, id uuid.UUID, body string, actor *uuid.UUID) (*QAIssueEvent, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrInvalidInput
	}
	var out QAIssueEvent
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out, `
			INSERT INTO qa_issue_events (org_id, issue_id, kind, body, actor_id)
			VALUES ($1, $2, 'comment', $3, $4) RETURNING `+qaEventCols, s.OrgID(), id, body, actor); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE qa_issues SET updated_at = now() WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQAIssueEvents(ctx context.Context, s tenant.Scope, id uuid.UUID) ([]QAIssueEvent, error) {
	var out []QAIssueEvent
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out,
			`SELECT `+qaEventCols+` FROM qa_issue_events WHERE issue_id = $1 ORDER BY created_at, id`, id)
	})
	return out, err
}

func LinkQAIssue(ctx context.Context, s tenant.Scope, itemID, issueID uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var projectID uuid.UUID
		if err := tx.GetContext(ctx, &projectID, `SELECT project_id FROM qa_issues WHERE id = $1`, issueID); err != nil {
			return err
		}
		if _, err := itemPlanInProject(ctx, tx, itemID, projectID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO qa_issue_links (org_id, item_id, issue_id) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, s.OrgID(), itemID, issueID)
		return err
	})
	return qaWriteErr(err)
}

func UnlinkQAIssue(ctx context.Context, s tenant.Scope, itemID, issueID uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM qa_issue_links WHERE item_id = $1 AND issue_id = $2`, itemID, issueID)
		return err
	})
}

func ListQAIssuesForItem(ctx context.Context, s tenant.Scope, itemID uuid.UUID) ([]QAIssue, error) {
	var out []QAIssue
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaIssueCols+` FROM qa_issues WHERE id IN (SELECT issue_id FROM qa_issue_links WHERE item_id = $1)
			ORDER BY key`, itemID)
	})
	return out, err
}

func ListQAItemsForIssue(ctx context.Context, s tenant.Scope, issueID uuid.UUID) ([]QARunItem, error) {
	var out []QARunItem
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaItemCols+` FROM qa_run_items i
			WHERE i.id IN (SELECT item_id FROM qa_issue_links WHERE issue_id = $1)
			ORDER BY i.run_id, i.position`, issueID)
	})
	return out, err
}
