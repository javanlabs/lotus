package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// QASection is one node of a project's test case library tree.
type QASection struct {
	ID         uuid.UUID  `db:"id"`
	OrgID      uuid.UUID  `db:"org_id"`
	ProjectID  uuid.UUID  `db:"project_id"`
	ParentID   *uuid.UUID `db:"parent_id"`
	Name       string     `db:"name"`
	SortOrder  int        `db:"sort_order"`
	ArchivedAt *time.Time `db:"archived_at"`
	CreatedAt  time.Time  `db:"created_at"`
	// PicID is the person in charge of the section's cases, unless a case
	// (or a nearer section) names its own; see QAEffectivePIC.
	PicID *uuid.UUID `db:"pic_id"`
}

const qaSectionCols = `id, org_id, project_id, parent_id, name, sort_order, archived_at, created_at, pic_id`

// lockQASections serialises tree changes within one project, so two moves
// cannot together form a cycle.
func lockQASections(ctx context.Context, tx *sqlx.Tx, projectID uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "qa_sections:"+projectID.String())
	return err
}

// qaSectionDepth returns the depth of id (top level = 1).
func qaSectionDepth(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) (int, error) {
	var depth int
	err := tx.GetContext(ctx, &depth, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id, 1 AS depth FROM qa_sections WHERE id = $1
			UNION ALL
			SELECT s.id, s.parent_id, up.depth + 1 FROM qa_sections s JOIN up ON s.id = up.parent_id
		) SELECT COALESCE(max(depth), 0) FROM up`, id)
	return depth, err
}

// qaSubtreeHeight returns how many levels id and its descendants span (a leaf = 1).
func qaSubtreeHeight(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) (int, error) {
	var h int
	err := tx.GetContext(ctx, &h, `
		WITH RECURSIVE down AS (
			SELECT id, 1 AS h FROM qa_sections WHERE id = $1
			UNION ALL
			SELECT s.id, down.h + 1 FROM qa_sections s JOIN down ON s.parent_id = down.id
		) SELECT COALESCE(max(h), 0) FROM down`, id)
	return h, err
}

func CreateQASection(ctx context.Context, s tenant.Scope, projectID uuid.UUID, name string, parentID *uuid.UUID) (*QASection, error) {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return nil, err
	}
	var out QASection
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockQASections(ctx, tx, projectID); err != nil {
			return err
		}
		if parentID != nil {
			// The foreign key only pins the organization; the parent must
			// also be in this project.
			if err := liveSectionInProject(ctx, tx, *parentID, projectID); err != nil {
				return err
			}
			d, err := qaSectionDepth(ctx, tx, *parentID)
			if err != nil {
				return err
			}
			if d == 0 {
				return ErrNotFound
			}
			if d+1 > MaxQASectionDepth {
				return ErrQASectionTooDeep
			}
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_sections (org_id, project_id, parent_id, name)
			VALUES ($1, $2, $3, $4)
			RETURNING `+qaSectionCols, s.OrgID(), projectID, parentID, name)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQASections(ctx context.Context, s tenant.Scope, projectID uuid.UUID) ([]QASection, error) {
	var out []QASection
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaSectionCols+` FROM qa_sections
			WHERE project_id = $1 ORDER BY sort_order, lower(name)`, projectID)
	})
	return out, err
}

func RenameQASection(ctx context.Context, s tenant.Scope, id uuid.UUID, name string) error {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return err
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_sections SET name = $2 WHERE id = $1`, id, name)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

// SetQASectionPIC sets (or clears, with nil) the section's PIC. Who may be
// PIC is the caller's check: the database only knows the user exists.
func SetQASectionPIC(ctx context.Context, s tenant.Scope, id uuid.UUID, pic *uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_sections SET pic_id = $2 WHERE id = $1`, id, pic)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

// QAEffectivePIC is who is in charge of a case filed in sectionID: its own
// PIC when it has one, else the nearest section up the tree that names one.
// from is that section (nil when the PIC is the case's own, or nobody's).
// The case list's PIC filter applies the same rule in SQL (qaCaseWhere).
func QAEffectivePIC(sections []QASection, sectionID uuid.UUID, own *uuid.UUID) (pic *uuid.UUID, from *QASection) {
	if own != nil {
		return own, nil
	}
	byID := make(map[uuid.UUID]int, len(sections))
	for i, sec := range sections {
		byID[sec.ID] = i
	}
	id := &sectionID
	for range MaxQASectionDepth + 1 {
		i, ok := byID[*id]
		if !ok {
			return nil, nil
		}
		if sections[i].PicID != nil {
			return sections[i].PicID, &sections[i]
		}
		if id = sections[i].ParentID; id == nil {
			return nil, nil
		}
	}
	return nil, nil
}

func MoveQASection(ctx context.Context, s tenant.Scope, id uuid.UUID, parentID *uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var projectID uuid.UUID
		if err := tx.GetContext(ctx, &projectID, `SELECT project_id FROM qa_sections WHERE id = $1`, id); err != nil {
			return err
		}
		if err := lockQASections(ctx, tx, projectID); err != nil {
			return err
		}
		parentDepth := 0
		if parentID != nil {
			if *parentID == id {
				return ErrQASectionCycle
			}
			if err := liveSectionInProject(ctx, tx, *parentID, projectID); err != nil {
				return err
			}
			var inside bool
			if err := tx.GetContext(ctx, &inside, `
				WITH RECURSIVE up AS (
					SELECT id, parent_id FROM qa_sections WHERE id = $1
					UNION ALL
					SELECT s.id, s.parent_id FROM qa_sections s JOIN up ON s.id = up.parent_id
				) SELECT EXISTS (SELECT 1 FROM up WHERE id = $2)`, *parentID, id); err != nil {
				return err
			}
			if inside {
				return ErrQASectionCycle
			}
			d, err := qaSectionDepth(ctx, tx, *parentID)
			if err != nil {
				return err
			}
			if d == 0 {
				return ErrNotFound
			}
			parentDepth = d
		}
		h, err := qaSubtreeHeight(ctx, tx, id)
		if err != nil {
			return err
		}
		if parentDepth+h > MaxQASectionDepth {
			return ErrQASectionTooDeep
		}
		res, err := tx.ExecContext(ctx, `UPDATE qa_sections SET parent_id = $2 WHERE id = $1`, id, parentID)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

// ArchiveQASection archives the section, its subsections and every case in them.
func ArchiveQASection(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			WITH RECURSIVE down AS (
				SELECT id FROM qa_sections WHERE id = $1
				UNION ALL
				SELECT s.id FROM qa_sections s JOIN down ON s.parent_id = down.id
			), cases AS (
				UPDATE qa_cases SET archived_at = now()
				WHERE section_id IN (SELECT id FROM down) AND archived_at IS NULL
			)
			UPDATE qa_sections SET archived_at = now()
			WHERE id IN (SELECT id FROM down) AND archived_at IS NULL`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}
