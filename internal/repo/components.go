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

type Component struct {
	ID         uuid.UUID  `db:"id"`
	OrgID      uuid.UUID  `db:"org_id"`
	ProjectID  uuid.UUID  `db:"project_id"`
	Slug       string     `db:"slug"`
	Name       string     `db:"name"`
	Kind       string     `db:"kind"`
	Language   string     `db:"language"`
	ArchivedAt *time.Time `db:"archived_at"`
	SortOrder  int        `db:"sort_order"`
	CreatedAt  time.Time  `db:"created_at"`
}

type NewComponent struct {
	ProjectID uuid.UUID
	Slug      string
	Name      string
	Kind      string
	Language  string
}

// CreateComponent inserts under ProjectID. The composite FK
// (project_id, org_id) REFERENCES projects(id, org_id) rejects this
// outright if ProjectID belongs to another organization — no application
// check needed for that case, only for "does it exist at all" (surfaced
// as a foreign_key_violation, mapped to ErrNotFound below since either
// way the caller gave a project id this org can't use).
func CreateComponent(ctx context.Context, s tenant.Scope, in NewComponent) (*Component, error) {
	var out Component
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO components (org_id, project_id, slug, name, kind, language)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, org_id, project_id, slug, name, kind, language, archived_at, sort_order, created_at`,
			s.OrgID(), in.ProjectID, in.Slug, in.Name, in.Kind, in.Language)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return nil, ErrSlugTaken
			case "23503":
				return nil, ErrNotFound
			case "23514":
				return nil, ErrInvalidInput
			}
		}
		return nil, err
	}
	return &out, nil
}

func ComponentByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Component, error) {
	var out Component
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT id, org_id, project_id, slug, name, kind, language, archived_at, sort_order, created_at
			FROM components WHERE id = $1`, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// ComponentBySlug finds a component by its slug, unique within its
// project.
func ComponentBySlug(ctx context.Context, s tenant.Scope, projectID uuid.UUID, slug string) (*Component, error) {
	var out Component
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT id, org_id, project_id, slug, name, kind, language, archived_at, sort_order, created_at
			FROM components WHERE project_id = $1 AND slug = $2`, projectID, slug)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func ListComponents(ctx context.Context, s tenant.Scope, projectID uuid.UUID, includeArchived bool) ([]Component, error) {
	var out []Component
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		q := `SELECT id, org_id, project_id, slug, name, kind, language, archived_at, sort_order, created_at
			FROM components WHERE project_id = $1`
		if !includeArchived {
			q += ` AND archived_at IS NULL`
		}
		q += ` ORDER BY sort_order, created_at`
		return tx.SelectContext(ctx, &out, q, projectID)
	})
	return out, err
}

// ComponentUpdate deliberately excludes ProjectID and Kind: task 2.1 only
// calls for renaming (name/slug) and reordering to be mutable in place —
// moving a component to a different project, or changing what kind of
// thing it is, isn't a requirement here and reopens questions (does its
// history move with it?) this phase doesn't answer.
type ComponentUpdate struct {
	Slug      string
	Name      string
	Language  string
	SortOrder int
}

func UpdateComponent(ctx context.Context, s tenant.Scope, id uuid.UUID, in ComponentUpdate) (*Component, error) {
	var out Component
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			UPDATE components SET slug = $2, name = $3, language = $4, sort_order = $5
			WHERE id = $1
			RETURNING id, org_id, project_id, slug, name, kind, language, archived_at, sort_order, created_at`,
			id, in.Slug, in.Name, in.Language, in.SortOrder)
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

// SetComponentArchived archives or restores a component. Its project's
// status for today is rewritten at once (an archived component no longer
// counts); earlier days keep listing it as it was.
func SetComponentArchived(ctx context.Context, s tenant.Scope, id uuid.UUID, archived bool) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		var expr string
		if archived {
			expr = "now()"
		} else {
			expr = "NULL"
		}
		var projectID uuid.UUID
		err := tx.GetContext(ctx, &projectID, `UPDATE components SET archived_at = `+expr+` WHERE id = $1 RETURNING project_id`, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return recordProjectDayTx(ctx, tx, projectID)
	})
}

type ComponentDeletionImpact struct {
	Bindings int `db:"bindings"`
}

func ComponentDeletionImpactOf(ctx context.Context, s tenant.Scope, id uuid.UUID) (*ComponentDeletionImpact, error) {
	var out ComponentDeletionImpact
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT count(*) AS bindings FROM metric_bindings WHERE component_id = $1 AND deleted_at IS NULL`, id)
	})
	return &out, err
}

// HardDeleteComponent permanently removes a component and its metric
// bindings, unbinding (never deleting) any discovered_resources that
// pointed at those bindings — mirrors HardDeleteProject's cascade at one
// level down. Its project's status for today is rewritten without it;
// earlier days keep it.
func HardDeleteComponent(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		projectID, err := componentProjectID(ctx, tx, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE discovered_resources SET bound_binding_id = NULL
			WHERE bound_binding_id IN (SELECT id FROM metric_bindings WHERE component_id = $1)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM metric_bindings WHERE component_id = $1`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM components WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(res); err != nil {
			return err
		}
		return recordProjectDayTx(ctx, tx, projectID)
	})
}
