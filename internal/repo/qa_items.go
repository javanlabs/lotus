package repo

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

var qaResultStatuses = []string{"passed", "failed", "blocked", "skipped"}

type QARunItem struct {
	ID           uuid.UUID  `db:"id"`
	OrgID        uuid.UUID  `db:"org_id"`
	RunID        uuid.UUID  `db:"run_id"`
	CaseID       uuid.UUID  `db:"case_id"`
	GroupName    string     `db:"group_name"`
	Position     int        `db:"position"`
	Title        string     `db:"title"`
	Precondition string     `db:"precondition"`
	Steps        QASteps    `db:"steps"`
	CaseVersion  int        `db:"case_version"`
	AssigneeID   *uuid.UUID `db:"assignee_id"`
	Status       string     `db:"status"`
	StatusAt     *time.Time `db:"status_at"`
	ExecutedBy   *uuid.UUID `db:"executed_by"`
}

const qaItemCols = `i.id, i.org_id, i.run_id, i.case_id, i.group_name, i.position, i.title, i.precondition,
	i.steps, i.case_version, i.assignee_id, i.status, i.status_at, i.executed_by`

type QAItemFilter struct {
	AssigneeID *uuid.UUID
	Unassigned bool
	Status     string
	GroupName  *string
}

func ListQARunItems(ctx context.Context, s tenant.Scope, runID uuid.UUID, f QAItemFilter) ([]QARunItem, error) {
	var out []QARunItem
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaItemCols+` FROM qa_run_items i
			WHERE i.run_id = $1
			  AND ($2::uuid IS NULL OR i.assignee_id = $2)
			  AND (NOT $3 OR i.assignee_id IS NULL)
			  AND ($4 = '' OR i.status = $4)
			  AND ($5::text IS NULL OR i.group_name = $5)
			ORDER BY i.position`, runID, f.AssigneeID, f.Unassigned, f.Status, f.GroupName)
	})
	return out, err
}

func GetQARunItem(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QARunItem, error) {
	var out QARunItem
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaItemCols+` FROM qa_run_items i WHERE i.id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

// AssignQARunItems sets (or clears, with nil) the assignee of the given
// items of runID in one statement. Ids from other runs are ignored.
func AssignQARunItems(ctx context.Context, s tenant.Scope, runID uuid.UUID, itemIDs []uuid.UUID, assignee *uuid.UUID) (int, error) {
	var n int64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE qa_run_items SET assignee_id = $3 WHERE run_id = $1 AND id = ANY($2)`, runID, itemIDs, assignee)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), qaWriteErr(err)
}

type QAResult struct {
	ID        uuid.UUID  `db:"id"`
	OrgID     uuid.UUID  `db:"org_id"`
	ItemID    uuid.UUID  `db:"item_id"`
	Status    string     `db:"status"`
	Comment   string     `db:"comment"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

type QAResultInput struct {
	ItemID    uuid.UUID
	Status    string
	Comment   string
	CreatedBy *uuid.UUID
}

func RecordQAResult(ctx context.Context, s tenant.Scope, in QAResultInput) (*QAResult, error) {
	if !slices.Contains(qaResultStatuses, in.Status) {
		return nil, ErrInvalidInput
	}
	comment := strings.TrimSpace(in.Comment)
	if utf8.RuneCountInString(comment) > MaxQACommentLength {
		return nil, ErrQACommentTooLong
	}
	var out QAResult
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var closed bool
		if err := tx.GetContext(ctx, &closed, `
			SELECT r.closed_at IS NOT NULL FROM qa_run_items i JOIN qa_runs r ON r.id = i.run_id
			WHERE i.id = $1 FOR UPDATE OF i FOR SHARE OF r`, in.ItemID); err != nil {
			return err
		}
		if closed {
			return ErrQAClosed
		}
		if err := tx.GetContext(ctx, &out, `
			INSERT INTO qa_results (org_id, item_id, status, comment, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, clock_timestamp())
			RETURNING id, org_id, item_id, status, comment, created_by, created_at`,
			s.OrgID(), in.ItemID, in.Status, comment, in.CreatedBy); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE qa_run_items SET status = $2, status_at = $3, executed_by = $4 WHERE id = $1`,
			in.ItemID, in.Status, out.CreatedAt, in.CreatedBy)
		return err
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQAResults(ctx context.Context, s tenant.Scope, itemID uuid.UUID) ([]QAResult, error) {
	var out []QAResult
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, item_id, status, comment, created_by, created_at
			FROM qa_results WHERE item_id = $1 ORDER BY created_at DESC, id DESC`, itemID)
	})
	return out, err
}

// LatestQAResults is each item's most recent result, for the items of
// itemIDs that have one, in one query.
func LatestQAResults(ctx context.Context, s tenant.Scope, itemIDs []uuid.UUID) (map[uuid.UUID]QAResult, error) {
	out := make(map[uuid.UUID]QAResult, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	var rows []QAResult
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT DISTINCT ON (item_id) id, org_id, item_id, status, comment, created_by, created_at
			FROM qa_results WHERE item_id = ANY($1) ORDER BY item_id, created_at DESC, id DESC`, itemIDs)
	})
	for _, r := range rows {
		out[r.ItemID] = r
	}
	return out, err
}

type QAProgress struct {
	Total, Untested, Passed, Failed, Blocked, Skipped int
}

func (p QAProgress) Done() int { return p.Total - p.Untested }

// Count adds one item or case with status.
func (p *QAProgress) Count(status string) { p.add(status, 1) }

func (p *QAProgress) add(status string, n int) {
	p.Total += n
	switch status {
	case "untested":
		p.Untested += n
	case "passed":
		p.Passed += n
	case "failed":
		p.Failed += n
	case "blocked":
		p.Blocked += n
	case "skipped":
		p.Skipped += n
	}
}

func QARunProgress(ctx context.Context, s tenant.Scope, runID uuid.UUID) (QAProgress, map[uuid.UUID]QAProgress, error) {
	var rows []struct {
		Assignee *uuid.UUID `db:"assignee_id"`
		Status   string     `db:"status"`
		N        int        `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT assignee_id, status, count(*) AS n FROM qa_run_items
			WHERE run_id = $1 GROUP BY assignee_id, status`, runID)
	})
	var total QAProgress
	per := map[uuid.UUID]QAProgress{}
	for _, r := range rows {
		total.add(r.Status, r.N)
		key := uuid.Nil
		if r.Assignee != nil {
			key = *r.Assignee
		}
		p := per[key]
		p.add(r.Status, r.N)
		per[key] = p
	}
	return total, per, err
}

type QAMyItem struct {
	QARunItem
	RunName     string    `db:"run_name"`
	PlanID      uuid.UUID `db:"plan_id"`
	PlanName    string    `db:"plan_name"`
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
}

// ListMyQAItems lists untested items assigned to userID in open runs of open
// plans. projectIDs nil means every project (members and above); a guest
// passes its project list, and an empty list returns nothing.
// CountMyQAItems is len(ListMyQAItems(...)) without reading the items: the
// sidebar's count on every page.
func CountMyQAItems(ctx context.Context, s tenant.Scope, userID uuid.UUID, projectIDs []uuid.UUID) (int, error) {
	var n int
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, `
			SELECT count(*) FROM qa_run_items i
			JOIN qa_runs r ON r.id = i.run_id AND r.closed_at IS NULL
			JOIN qa_plans p ON p.id = r.plan_id AND p.closed_at IS NULL
			WHERE i.assignee_id = $1 AND i.status = 'untested'
			  AND ($2::uuid[] IS NULL OR p.project_id = ANY($2))`, userID, projectIDs)
	})
	return n, err
}

func ListMyQAItems(ctx context.Context, s tenant.Scope, userID uuid.UUID, projectIDs []uuid.UUID) ([]QAMyItem, error) {
	var out []QAMyItem
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaItemCols+`, r.name AS run_name, p.id AS plan_id, p.name AS plan_name,
				pr.id AS project_id, pr.name AS project_name
			FROM qa_run_items i
			JOIN qa_runs r ON r.id = i.run_id AND r.closed_at IS NULL
			JOIN qa_plans p ON p.id = r.plan_id AND p.closed_at IS NULL
			JOIN projects pr ON pr.id = p.project_id
			WHERE i.assignee_id = $1 AND i.status = 'untested'
			  AND ($2::uuid[] IS NULL OR p.project_id = ANY($2))
			ORDER BY pr.name, p.created_at, r.cycle, i.position`, userID, projectIDs)
	})
	return out, err
}
