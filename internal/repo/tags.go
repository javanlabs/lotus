package repo

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// Tags group an organization's projects; a project carries any number
// (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §5).
// Names are shown as written and unique per organization ignoring case.

// MaxTagNameLength bounds a tag name, in characters. The database CHECK
// enforces the same number.
const MaxTagNameLength = 40

// ErrInvalidTag: the name is blank or longer than MaxTagNameLength.
var ErrInvalidTag = errors.New("tag name must be 1–40 characters")

// Tag is one of an organization's tags.
type Tag struct {
	ID   uuid.UUID `db:"id"`
	Name string    `db:"name"`
}

// ListTags is every tag carried by at least one live project, by name
// ignoring case. A tag only deleted projects carry is left out, so the
// filter never offers a chip that matches nothing.
func ListTags(ctx context.Context, s tenant.Scope) ([]Tag, error) {
	out := []Tag{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT t.id, t.name FROM tags t
			WHERE EXISTS (
				SELECT 1 FROM project_tags pt JOIN projects p ON p.id = pt.project_id
				WHERE pt.tag_id = t.id AND p.deleted_at IS NULL)
			ORDER BY lower(t.name), t.id`)
	})
	return out, err
}

// ProjectTagsByProject is every live project's tags, keyed by project,
// each slice by name ignoring case. A project with no tags is absent.
func ProjectTagsByProject(ctx context.Context, s tenant.Scope) (map[uuid.UUID][]Tag, error) {
	var rows []struct {
		ProjectID uuid.UUID `db:"project_id"`
		Tag
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT pt.project_id, t.id, t.name
			FROM project_tags pt
			JOIN tags t ON t.id = pt.tag_id
			JOIN projects p ON p.id = pt.project_id
			WHERE p.deleted_at IS NULL
			ORDER BY pt.project_id, lower(t.name), t.id`)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]Tag)
	for _, r := range rows {
		out[r.ProjectID] = append(out[r.ProjectID], r.Tag)
	}
	return out, nil
}

// AddProjectTag puts the tag called name on a live project and returns it.
// The name is trimmed; an existing tag matched ignoring case is reused
// with its stored spelling, so ` project ` lands on PROJECT rather than
// creating a second one. Adding a tag the project already carries is a
// no-op. A project this organization cannot see is ErrNotFound.
func AddProjectTag(ctx context.Context, s tenant.Scope, projectID uuid.UUID, name string) (Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxTagNameLength {
		return Tag{}, ErrInvalidTag
	}
	var out Tag
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var live bool
		if err := tx.GetContext(ctx, &live, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND deleted_at IS NULL)`, projectID); err != nil {
			return err
		}
		if !live {
			return ErrNotFound
		}
		// ON CONFLICT rather than look-then-insert: two people adding the
		// same new tag at once both end up on the one row.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tags (org_id, name) VALUES ($1, $2)
			ON CONFLICT (org_id, lower(name)) DO NOTHING`, s.OrgID(), name); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out, `SELECT id, name FROM tags WHERE lower(name) = lower($1)`, name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO project_tags (org_id, project_id, tag_id) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, s.OrgID(), projectID, out.ID)
		return err
	})
	if err != nil {
		return Tag{}, err
	}
	return out, nil
}

// RemoveProjectTag takes a tag off a project. When no project carries the
// tag any more it is deleted in the same transaction, so a tag nobody uses
// does not linger. A link that does not exist is ErrNotFound.
func RemoveProjectTag(ctx context.Context, s tenant.Scope, projectID, tagID uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM project_tags WHERE project_id = $1 AND tag_id = $2`, projectID, tagID)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(res); err != nil {
			return err
		}
		// A deleted project's link still counts: restoring the project by
		// hand should bring its tags back with it.
		_, err = tx.ExecContext(ctx, `
			DELETE FROM tags t WHERE t.id = $1
			AND NOT EXISTS (SELECT 1 FROM project_tags pt WHERE pt.tag_id = t.id)`, tagID)
		return err
	})
}

// ProjectIDsWithAnyTag is every live project carrying at least one of
// tagIDs. No tags matches nothing.
func ProjectIDsWithAnyTag(ctx context.Context, s tenant.Scope, tagIDs []uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	if len(tagIDs) == 0 {
		return out, nil
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT DISTINCT p.id FROM projects p
			JOIN project_tags pt ON pt.project_id = p.id
			WHERE p.deleted_at IS NULL AND pt.tag_id = ANY($1)`, tagIDs)
	})
	return out, err
}
