package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// QACaseImport is one case of a bulk import: the section path it goes in,
// created as needed, and the case (its SectionID is ignored).
type QACaseImport struct {
	SectionPath []string
	Case        QACaseInput
}

// QASectionArchivedError: an import path runs through an archived section.
// Cases are never filed into an archived section, and a sibling with the
// same name cannot be created beside it.
type QASectionArchivedError struct{ Path string }

func (e *QASectionArchivedError) Error() string { return fmt.Sprintf("section %q is archived", e.Path) }

// ImportQACases creates every case in rows, in order, in one transaction:
// all of them or none. Sections along each path are matched by name
// ignoring case, as the sibling-name index does, and created when missing.
// Keys continue from the project's highest. It returns how many cases and
// sections it created.
func ImportQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, rows []QACaseImport, createdBy *uuid.UUID) (cases, sections int, err error) {
	keys, sections, err := ImportQACasesKeys(ctx, s, projectID, rows, createdBy)
	return len(keys), sections, err
}

// ImportQACasesKeys is ImportQACases returning the created cases' keys, in
// row order. They are consecutive: key allocation is locked per project
// for the whole transaction.
func ImportQACasesKeys(ctx context.Context, s tenant.Scope, projectID uuid.UUID, rows []QACaseImport, createdBy *uuid.UUID) (keys []int, sections int, err error) {
	inputs := make([]QACaseInput, len(rows))
	for i, r := range rows {
		if len(r.SectionPath) == 0 || len(r.SectionPath) > MaxQASectionDepth {
			return nil, 0, ErrInvalidInput
		}
		if inputs[i], err = r.Case.normalized(); err != nil {
			return nil, 0, err
		}
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := lockQASections(ctx, tx, projectID); err != nil {
			return err
		}
		var existing []QASection
		if err := tx.SelectContext(ctx, &existing, `SELECT `+qaSectionCols+` FROM qa_sections WHERE project_id = $1`, projectID); err != nil {
			return err
		}
		type node struct {
			id       uuid.UUID
			archived bool
		}
		byName := map[string]node{} // parent id (or "") + "/" + lower(name)
		keyOf := func(parent *uuid.UUID, name string) string {
			p := ""
			if parent != nil {
				p = parent.String()
			}
			return p + "/" + strings.ToLower(name)
		}
		for _, sec := range existing {
			byName[keyOf(sec.ParentID, sec.Name)] = node{sec.ID, sec.ArchivedAt != nil}
		}
		key, err := nextQAKey(ctx, tx, "qa_cases", projectID)
		if err != nil {
			return err
		}
		for i, r := range rows {
			var parent *uuid.UUID
			for depth, raw := range r.SectionPath {
				name, err := normalizeQAName(raw, MaxQANameLength)
				if err != nil {
					return err
				}
				k := keyOf(parent, name)
				n, ok := byName[k]
				if ok && n.archived {
					return &QASectionArchivedError{Path: strings.Join(r.SectionPath[:depth+1], " / ")}
				}
				if !ok {
					if err := tx.GetContext(ctx, &n.id, `
						INSERT INTO qa_sections (org_id, project_id, parent_id, name) VALUES ($1, $2, $3, $4) RETURNING id`,
						s.OrgID(), projectID, parent, name); err != nil {
						return err
					}
					byName[k] = n
					sections++
				}
				id := n.id
				parent = &id
			}
			in := inputs[i]
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO qa_cases (org_id, project_id, section_id, key, title, precondition, notes, steps, priority, negative, created_by, pic_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				s.OrgID(), projectID, *parent, key, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, createdBy, in.PicID); err != nil {
				return err
			}
			keys = append(keys, key)
			key++
		}
		return nil
	})
	if err != nil {
		return nil, 0, qaWriteErr(err)
	}
	return keys, sections, nil
}
