package repo

import (
	"context"
	"fmt"
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
	PicID        *uuid.UUID `db:"pic_id"` // the case's own PIC; see QAEffectivePIC
}

const qaCaseCols = `id, org_id, project_id, section_id, key, title, precondition, notes, steps,
	priority, negative, version, archived_at, created_by, created_at, updated_at, pic_id`

type QACaseInput struct {
	SectionID    uuid.UUID
	Title        string
	Precondition string
	Notes        string
	Steps        QASteps
	Priority     *string
	Negative     bool
	PicID        *uuid.UUID // nil: in the charge of its section's PIC
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
		if err := liveSectionInProject(ctx, tx, in.SectionID, projectID); err != nil {
			return err
		}
		key, err := nextQAKey(ctx, tx, "qa_cases", projectID)
		if err != nil {
			return err
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_cases (org_id, project_id, section_id, key, title, precondition, notes, steps, priority, negative, created_by, pic_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING `+qaCaseCols,
			s.OrgID(), projectID, in.SectionID, key, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, createdBy, in.PicID)
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
	SectionID *uuid.UUID
	// IncludeSubsections widens SectionID to its whole subtree, which is
	// what picking a section in the library means.
	IncludeSubsections bool
	IncludeArchived    bool
	Query              string // matches title, case-insensitive, literally
	// NotInScenario leaves out the cases that scenario already holds (the
	// Add cases picker), before Limit applies.
	NotInScenario *uuid.UUID
	// PICID keeps the cases in that person's charge, PICNone those in
	// nobody's — by effective PIC: the case's own, else its nearest
	// section's (QAEffectivePIC).
	PICID         *uuid.UUID
	PICNone       bool
	Limit, Offset int // Limit 0 = every row
}

// qaCaseWhere is the filter both ListQACases and CountQACases apply. The
// recursive CTE scope is empty when no section is chosen; pics walks the
// project's tree from the top, handing each section its effective PIC, and
// is only read when a PIC filter is set.
const qaCaseWhere = `
	WITH RECURSIVE scope AS (
		SELECT id FROM qa_sections WHERE id = $2
		UNION ALL
		SELECT sec.id FROM qa_sections sec JOIN scope ON sec.parent_id = scope.id WHERE $5
	), pics AS (
		SELECT id, pic_id FROM qa_sections WHERE project_id = $1 AND parent_id IS NULL AND ($7::uuid IS NOT NULL OR $8)
		UNION ALL
		SELECT sec.id, COALESCE(sec.pic_id, pics.pic_id) FROM qa_sections sec JOIN pics ON sec.parent_id = pics.id
	)
	SELECT %s FROM qa_cases
	WHERE project_id = $1
	  AND ($2::uuid IS NULL OR section_id IN (SELECT id FROM scope))
	  AND ($3 OR archived_at IS NULL)
	  AND ($4 = '' OR title ILIKE '%%' || $4 || '%%' ESCAPE '\')
	  AND ($6::uuid IS NULL OR NOT EXISTS (
		SELECT 1 FROM qa_scenario_items si WHERE si.scenario_id = $6 AND si.case_id = qa_cases.id))
	  AND ($7::uuid IS NULL OR $7 = COALESCE(pic_id, (SELECT pics.pic_id FROM pics WHERE pics.id = section_id)))
	  AND (NOT $8 OR COALESCE(pic_id, (SELECT pics.pic_id FROM pics WHERE pics.id = section_id)) IS NULL)`

func (f QACaseFilter) args(projectID uuid.UUID) []any {
	return []any{projectID, f.SectionID, f.IncludeArchived, likeEscape(strings.TrimSpace(f.Query)), f.IncludeSubsections, f.NotInScenario,
		f.PICID, f.PICNone}
}

// likeEscape makes % and _ in a search box match themselves.
func likeEscape(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

func ListQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, f QACaseFilter) ([]QACase, error) {
	var out []QACase
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		q := fmt.Sprintf(qaCaseWhere, qaCaseCols) + ` ORDER BY key LIMIT NULLIF($9, 0) OFFSET $10`
		return tx.SelectContext(ctx, &out, q, append(f.args(projectID), f.Limit, f.Offset)...)
	})
	return out, err
}

func CountQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, f QACaseFilter) (int, error) {
	var n int
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, fmt.Sprintf(qaCaseWhere, "count(*)"), f.args(projectID)...)
	})
	return n, err
}

// QASectionCaseCounts is the number of live cases filed directly in each
// section of the project. Sections with none are absent.
func QASectionCaseCounts(ctx context.Context, s tenant.Scope, projectID uuid.UUID) (map[uuid.UUID]int, error) {
	var rows []struct {
		SectionID uuid.UUID `db:"section_id"`
		N         int       `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT section_id, count(*) AS n FROM qa_cases
			WHERE project_id = $1 AND archived_at IS NULL GROUP BY section_id`, projectID)
	})
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.SectionID] = r.N
	}
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
		// A case may stay where it is (an archived case in an archived
		// section can still be edited), but a move needs a live section.
		check := sectionInProject
		if in.SectionID != cur.SectionID {
			check = liveSectionInProject
		}
		if err := check(ctx, tx, in.SectionID, cur.ProjectID); err != nil {
			return err
		}
		version := cur.Version
		if cur.Title != in.Title || cur.Precondition != in.Precondition || !reflect.DeepEqual(cur.Steps, in.Steps) {
			version++
		}
		return tx.GetContext(ctx, &out, `
			UPDATE qa_cases SET section_id = $2, title = $3, precondition = $4, notes = $5, steps = $6,
				priority = $7, negative = $8, version = $9, pic_id = $10, updated_at = now()
			WHERE id = $1 RETURNING `+qaCaseCols,
			id, in.SectionID, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, version, in.PicID)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func setQACaseArchived(ctx context.Context, s tenant.Scope, id uuid.UUID, archived bool) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if !archived {
			var sectionArchived bool
			if err := tx.GetContext(ctx, &sectionArchived, `
				SELECT sec.archived_at IS NOT NULL FROM qa_cases c JOIN qa_sections sec ON sec.id = c.section_id
				WHERE c.id = $1`, id); err != nil {
				return err
			}
			if sectionArchived {
				return ErrQASectionArchived
			}
		}
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
		var archived *bool
		if err := tx.GetContext(ctx, &archived, `SELECT archived_at IS NOT NULL FROM qa_sections WHERE id = $1`, sectionID); err != nil {
			return err
		}
		if *archived {
			return ErrQASectionArchived
		}
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

// QACaseKeys is the key of each of ids that exists.
func QACaseKeys(ctx context.Context, s tenant.Scope, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	var rows []struct {
		ID  uuid.UUID `db:"id"`
		Key int       `db:"key"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `SELECT id, key FROM qa_cases WHERE id = ANY($1)`, ids)
	})
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Key
	}
	return out, err
}
