package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// ErrSlugTaken is returned by CreateProject/UpdateProject when the slug
// collides with another project in the same organization — slugs are
// unique per org, not globally (task 2.1), so the same "frontend" slug is
// fine in two different organizations.
var ErrSlugTaken = errors.New("slug already taken in this organization")

// ErrNotFound is returned by the single-row lookups when the id doesn't
// resolve to a row this Scope's organization can see — RLS makes a
// cross-org id behave identically to a nonexistent one, which is exactly
// the property task 1.4's 404-not-403 anti-enumeration pattern needs.
var ErrNotFound = errors.New("not found")

// ErrInvalidInput surfaces a database CHECK constraint violation (e.g. an
// unrecognized Component.Kind) as a caller error rather than a 500 — the
// constraint is still the actual authority, this just gives handlers a
// sentinel to map to 400 instead of leaking a raw Postgres error.
var ErrInvalidInput = errors.New("invalid input")

type Project struct {
	ID          uuid.UUID  `db:"id"`
	OrgID       uuid.UUID  `db:"org_id"`
	Slug        string     `db:"slug"`
	Name        string     `db:"name"`
	Description string     `db:"description"`
	ArchivedAt  *time.Time `db:"archived_at"`
	SortOrder   int        `db:"sort_order"`
	CreatedAt   time.Time  `db:"created_at"`
}

type NewProject struct {
	Slug        string
	Name        string
	Description string
}

func CreateProject(ctx context.Context, s tenant.Scope, in NewProject) (*Project, error) {
	var out Project
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO projects (org_id, slug, name, description)
			VALUES ($1, $2, $3, $4)
			RETURNING id, org_id, slug, name, description, archived_at, sort_order, created_at`,
			s.OrgID(), in.Slug, in.Name, in.Description)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrSlugTaken
		}
		return nil, err
	}
	return &out, nil
}

func ProjectByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Project, error) {
	var out Project
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT id, org_id, slug, name, description, archived_at, sort_order, created_at
			FROM projects WHERE id = $1`, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// ListProjects returns every project in the org, ordered for dashboard
// display (sort_order first, since that ordering is an editorial decision
// — task 2.1 — not alphabetical). includeArchived controls whether
// archived projects are included at all.
func ListProjects(ctx context.Context, s tenant.Scope, includeArchived bool) ([]Project, error) {
	var out []Project
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		q := `SELECT id, org_id, slug, name, description, archived_at, sort_order, created_at FROM projects`
		if !includeArchived {
			q += ` WHERE archived_at IS NULL`
		}
		q += ` ORDER BY sort_order, created_at`
		return tx.SelectContext(ctx, &out, q)
	})
	return out, err
}

type ProjectUpdate struct {
	Slug        string
	Name        string
	Description string
	SortOrder   int
}

func UpdateProject(ctx context.Context, s tenant.Scope, id uuid.UUID, in ProjectUpdate) (*Project, error) {
	var out Project
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			UPDATE projects SET slug = $2, name = $3, description = $4, sort_order = $5
			WHERE id = $1
			RETURNING id, org_id, slug, name, description, archived_at, sort_order, created_at`,
			id, in.Slug, in.Name, in.Description, in.SortOrder)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrSlugTaken
		}
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func SetProjectArchived(ctx context.Context, s tenant.Scope, id uuid.UUID, archived bool) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		var expr string
		if archived {
			expr = "now()"
		} else {
			expr = "NULL"
		}
		res, err := tx.ExecContext(ctx, `UPDATE projects SET archived_at = `+expr+` WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

// ProjectDeletionImpact reports how much would be lost by hard-deleting a
// project, for the confirmation prompt task 2.1 requires before that
// action is allowed. Snapshot history (metric_facts, task 3.7) doesn't
// exist yet in this phase, so that count is always 0 for now — see the
// TENSION task logged alongside this file.
type ProjectDeletionImpact struct {
	Components int `db:"components"`
	Bindings   int `db:"bindings"`
}

func ProjectDeletionImpactOf(ctx context.Context, s tenant.Scope, id uuid.UUID) (*ProjectDeletionImpact, error) {
	var out ProjectDeletionImpact
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT
				(SELECT count(*) FROM components WHERE project_id = $1) AS components,
				(SELECT count(*) FROM metric_bindings mb JOIN components c ON c.id = mb.component_id
				 WHERE c.project_id = $1 AND mb.deleted_at IS NULL) AS bindings`,
			id)
	})
	return &out, err
}

// HardDeleteProject permanently removes a project and everything under
// it: its components, their metric bindings, and unbinds (never deletes)
// any discovered_resources that pointed at those bindings. This is the
// admin-only, confirmation-gated escape hatch task 2.1 calls for — normal
// deletion is SetProjectArchived, which keeps history intact.
func HardDeleteProject(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE discovered_resources SET bound_binding_id = NULL
			WHERE bound_binding_id IN (
				SELECT mb.id FROM metric_bindings mb JOIN components c ON c.id = mb.component_id
				WHERE c.project_id = $1)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM metric_bindings WHERE component_id IN (SELECT id FROM components WHERE project_id = $1)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM components WHERE project_id = $1`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}
