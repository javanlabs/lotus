package global

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type Membership struct {
	OrgID     uuid.UUID `db:"org_id"`
	UserID    uuid.UUID `db:"user_id"`
	RoleID    uuid.UUID `db:"role_id"`
	RoleKey   string    `db:"role_key"`
	Status    string    `db:"status"`
	SSOExempt bool      `db:"sso_exempt"`
}

// ActiveMembership returns the user's membership in org — including its
// role key ("owner"/"admin"/"member"/"viewer"), joined in so callers never
// need a second query to turn role_id into something authz.Principal can
// use — or sql.ErrNoRows if they aren't a member at all or their
// membership isn't active. Task 1.2's handoff uses this as its sole gate —
// "is this user allowed into this organization at all"; task 1.4's
// authz.LoadPrincipal uses the same function to also learn what they're
// allowed to do once inside.
func ActiveMembership(ctx context.Context, db *sqlx.DB, userID, orgID uuid.UUID) (*Membership, error) {
	var out Membership
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT m.org_id, m.user_id, m.role_id, r.key AS role_key, m.status, m.sso_exempt
		FROM memberships m
		JOIN roles r ON r.id = m.role_id
		WHERE m.user_id = $1 AND m.org_id = $2 AND m.status = 'active'`, userID, orgID)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateMembershipRole changes a member's role and bumps the org's
// perm_epoch in the same transaction, so the staleness a live session
// picks up (task 1.3) is never observable one write ahead of the actual
// role change.
func UpdateMembershipRole(ctx context.Context, db *sqlx.DB, orgID, userID, roleID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
		UPDATE memberships SET role_id = $1 WHERE org_id = $2 AND user_id = $3`, roleID, orgID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE organizations SET perm_epoch = perm_epoch + 1 WHERE id = $1`, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveMembership deletes a membership and bumps the org's perm_epoch in
// the same transaction. Blocked by the database if this would remove the
// organization's last Owner (task 0.2's trigger).
func RemoveMembership(ctx context.Context, db *sqlx.DB, orgID, userID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE organizations SET perm_epoch = perm_epoch + 1 WHERE id = $1`, orgID); err != nil {
		return err
	}
	return tx.Commit()
}
