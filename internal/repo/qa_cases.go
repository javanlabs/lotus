package repo

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

type QACase struct {
	ID           uuid.UUID  `db:"id"`
	OrgID        uuid.UUID  `db:"org_id"`
	ProjectID    uuid.UUID  `db:"project_id"`
	SectionID    uuid.UUID  `db:"section_id"`
	Key          int        `db:"key"`
	Title        string     `db:"title"`
	Precondition string     `db:"precondition"`
	Notes        string     `db:"notes"`
	Steps        QASteps    `db:"steps"`
	Priority     *string    `db:"priority"`
	Negative     bool       `db:"negative"`
	Version      int        `db:"version"`
	ArchivedAt   *time.Time `db:"archived_at"`
	CreatedBy    *uuid.UUID `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

const qaCaseCols = `id, org_id, project_id, section_id, key, title, precondition, notes, steps,
	priority, negative, version, archived_at, created_by, created_at, updated_at`

type QACaseInput struct {
	SectionID    uuid.UUID
	Title        string
	Precondition string
	Notes        string
	Steps        QASteps
	Priority     *string
	Negative     bool
}

func (in QACaseInput) normalized() (QACaseInput, error) {
	t, err := normalizeQAName(in.Title, MaxQATitleLength)
	if err != nil {
		return in, err
	}
	in.Title = t
	in.Precondition = strings.TrimSpace(in.Precondition)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.Steps == nil {
		in.Steps = QASteps{}
	}
	return in, nil
}

func CreateQACase(ctx context.Context, s tenant.Scope, projectID uuid.UUID, in QACaseInput, createdBy *uuid.UUID) (*QACase, error) {
	in, err := in.normalized()
	if err != nil {
		return nil, err
	}
	var out QACase
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := sectionInProject(ctx, tx, in.SectionID, projectID); err != nil {
			return err
		}
		key, err := nextQAKey(ctx, tx, "qa_cases", projectID)
		if err != nil {
			return err
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_cases (org_id, project_id, section_id, key, title, precondition, notes, steps, priority, negative, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+qaCaseCols,
			s.OrgID(), projectID, in.SectionID, key, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, createdBy)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQACase(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QACase, error) {
	var out QACase
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaCaseCols+` FROM qa_cases WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQACaseByKey(ctx context.Context, s tenant.Scope, projectID uuid.UUID, key int) (*QACase, error) {
	var out QACase
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaCaseCols+` FROM qa_cases WHERE project_id = $1 AND key = $2`, projectID, key)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

type QACaseFilter struct {
	SectionID       *uuid.UUID
	IncludeArchived bool
	Query           string // matches title, case-insensitive
}

func ListQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, f QACaseFilter) ([]QACase, error) {
	var out []QACase
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaCaseCols+` FROM qa_cases
			WHERE project_id = $1
			  AND ($2::uuid IS NULL OR section_id = $2)
			  AND ($3 OR archived_at IS NULL)
			  AND ($4 = '' OR title ILIKE '%' || $4 || '%')
			ORDER BY key`, projectID, f.SectionID, f.IncludeArchived, strings.TrimSpace(f.Query))
	})
	return out, err
}

func UpdateQACase(ctx context.Context, s tenant.Scope, id uuid.UUID, in QACaseInput) (*QACase, error) {
	in, err := in.normalized()
	if err != nil {
		return nil, err
	}
	var out QACase
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		var cur QACase
		if err := tx.GetContext(ctx, &cur, `SELECT `+qaCaseCols+` FROM qa_cases WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		if err := sectionInProject(ctx, tx, in.SectionID, cur.ProjectID); err != nil {
			return err
		}
		version := cur.Version
		if cur.Title != in.Title || cur.Precondition != in.Precondition || !reflect.DeepEqual(cur.Steps, in.Steps) {
			version++
		}
		return tx.GetContext(ctx, &out, `
			UPDATE qa_cases SET section_id = $2, title = $3, precondition = $4, notes = $5, steps = $6,
				priority = $7, negative = $8, version = $9, updated_at = now()
			WHERE id = $1 RETURNING `+qaCaseCols,
			id, in.SectionID, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, version)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func setQACaseArchived(ctx context.Context, s tenant.Scope, id uuid.UUID, archived bool) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_cases SET archived_at = CASE WHEN $2 THEN now() ELSE NULL END, updated_at = now()
			WHERE id = $1`, id, archived)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

func ArchiveQACase(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return setQACaseArchived(ctx, s, id, true)
}

func RestoreQACase(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return setQACaseArchived(ctx, s, id, false)
}

// MoveQACases files every case in ids under sectionID. Cases from another
// project than the section's are left untouched; the count says how many moved.
func MoveQACases(ctx context.Context, s tenant.Scope, ids []uuid.UUID, sectionID uuid.UUID) (int, error) {
	var n int64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_cases c SET section_id = $2, updated_at = now()
			FROM qa_sections sec
			WHERE sec.id = $2 AND c.project_id = sec.project_id AND c.id = ANY($1)`, ids, sectionID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), qaWriteErr(err)
}
