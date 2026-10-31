package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// Project members list which projects a guest (a client doing UAT) may
// see. Members, admins and owners see every project and never need a row.

// AddProjectMember files userID on projectID. Adding twice is a no-op.
func AddProjectMember(ctx context.Context, s tenant.Scope, projectID, userID uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		// The project must be visible in this scope. Without this check a
		// second organization adding an existing (project, user) pair hits
		// ON CONFLICT DO NOTHING before the composite FK is ever checked,
		// and silently "succeeds".
		var visible bool
		if err := tx.GetContext(ctx, &visible,
			`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND deleted_at IS NULL)`, projectID); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO project_members (org_id, project_id, user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (project_id, user_id) DO NOTHING`,
			s.OrgID(), projectID, userID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrNotFound
	}
	return err
}

func RemoveProjectMember(ctx context.Context, s tenant.Scope, projectID, userID uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
		return err
	})
}

func IsProjectMember(ctx context.Context, s tenant.Scope, projectID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &ok, `
			SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`,
			projectID, userID)
	})
	return ok, err
}

func ListProjectMemberUserIDs(ctx context.Context, s tenant.Scope, projectID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &ids,
			`SELECT user_id FROM project_members WHERE project_id = $1 ORDER BY created_at`, projectID)
	})
	return ids, err
}

// ListProjectIDsForMember never returns a nil slice: callers pass the result
// to ListMyQAItems, where nil means "every project".
func ListProjectIDsForMember(ctx context.Context, s tenant.Scope, userID uuid.UUID) ([]uuid.UUID, error) {
	ids := []uuid.UUID{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &ids,
			`SELECT project_id FROM project_members WHERE user_id = $1 ORDER BY created_at`, userID)
	})
	return ids, err
}
