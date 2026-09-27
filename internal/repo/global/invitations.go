package global

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// ErrInvitationPending is returned by CreateInvitation when the target
// email already has a live (unaccepted, unrevoked) invitation to this org
// — task 0.2's partial unique index enforces "one pending invite per
// (org, email)" at the database level; this just gives that violation a
// name callers can check for instead of a bare Postgres error.
var ErrInvitationPending = errors.New("an invitation to this email is already pending")

// Invitation is the full row task 1.7's accept/decline/resend/revoke flow
// needs — CreateInvitation's callers (task 1.6) only need the token, so
// they don't get this full shape back.
type Invitation struct {
	ID         uuid.UUID  `db:"id"`
	OrgID      uuid.UUID  `db:"org_id"`
	Email      string     `db:"email"`
	RoleID     uuid.UUID  `db:"role_id"`
	RoleKey    string     `db:"role_key"`
	InvitedBy  uuid.UUID  `db:"invited_by"`
	ExpiresAt  time.Time  `db:"expires_at"`
	AcceptedAt *time.Time `db:"accepted_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
	DeclinedAt *time.Time `db:"declined_at"`
}

type NewInvitation struct {
	OrgID     uuid.UUID
	Email     string
	RoleKey   string
	InvitedBy uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	// ProjectIDs are the projects a guest will see once it accepts. Ignored
	// for other roles, who see every project.
	ProjectIDs []uuid.UUID
}

// CreateInvitation is the invite-creation path both task 1.6's onboarding
// screen and task 1.7's resend use. Returns the new invitation's id.
func CreateInvitation(ctx context.Context, db *sqlx.DB, in NewInvitation) (uuid.UUID, error) {
	var id uuid.UUID
	err := sqlx.GetContext(ctx, db, &id, `
		INSERT INTO invitations (org_id, email, role_id, invited_by, token_hash, expires_at, project_ids)
		SELECT $1, $2, r.id, $3, $4, $5, $7
		FROM roles r WHERE r.key = $6 AND r.org_id IS NULL
		RETURNING id`,
		in.OrgID, in.Email, in.InvitedBy, in.TokenHash, in.ExpiresAt, in.RoleKey, projectIDs(in.ProjectIDs))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return uuid.Nil, ErrInvitationPending
		}
		return uuid.Nil, err
	}
	return id, nil
}

// InvitationByTokenHash returns a still-usable invitation: unexpired,
// unaccepted, undeclined, unrevoked. Any of those states simply doesn't
// come back — callers show the same "this invite isn't valid" outcome for
// all of them (task 1.7's acceptance criteria: expired, already-used, and
// revoked tokens are each rejected, with distinguishable messages coming
// from the caller checking the specific field, not from this query).
func InvitationByTokenHash(ctx context.Context, db *sqlx.DB, tokenHash string) (*Invitation, error) {
	var out Invitation
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT i.id, i.org_id, i.email, i.role_id, r.key AS role_key, i.invited_by,
		       i.expires_at, i.accepted_at, i.revoked_at, i.declined_at
		FROM invitations i
		JOIN roles r ON r.id = i.role_id
		WHERE i.token_hash = $1
		  AND i.expires_at > now()
		  AND i.accepted_at IS NULL
		  AND i.revoked_at IS NULL
		  AND i.declined_at IS NULL`, tokenHash)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// projectIDs never hands Postgres a NULL array for the NOT NULL column.
func projectIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// AcceptInvitation marks the invitation accepted and creates the
// membership, in one transaction. If the invitee is already a member (a
// second invite accepted, or they joined some other way in the meantime),
// this is a no-op on the membership — accepting an invitation can only
// ever add access, never silently downgrade an existing role. A guest
// invitation also lists the invitee on its projects (those still in the
// organization), under the organization's row-level security.
func AcceptInvitation(ctx context.Context, db *sqlx.DB, invitationID, userID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var roleID, orgID uuid.UUID
	err = tx.QueryRowxContext(ctx, `
		UPDATE invitations SET accepted_at = now()
		WHERE id = $1 AND accepted_at IS NULL AND declined_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		RETURNING role_id, org_id`, invitationID).Scan(&roleID, &orgID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO NOTHING`, orgID, userID, roleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_members (org_id, project_id, user_id)
		SELECT $1, p.id, $3 FROM projects p
		JOIN invitations i ON i.id = $2 AND p.id = ANY(i.project_ids)
		JOIN roles r ON r.id = i.role_id AND r.key = 'guest'
		WHERE p.org_id = $1
		ON CONFLICT (project_id, user_id) DO NOTHING`, orgID, invitationID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE organizations SET perm_epoch = perm_epoch + 1 WHERE id = $1`, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeclineInvitation sets declined_at. Idempotent by construction: the
// WHERE clause only matches a still-pending row, so a second decline
// simply affects zero rows rather than erroring.
func DeclineInvitation(ctx context.Context, db *sqlx.DB, invitationID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		UPDATE invitations SET declined_at = now()
		WHERE id = $1 AND accepted_at IS NULL AND declined_at IS NULL AND revoked_at IS NULL`, invitationID)
	return err
}

// RevokeInvitation sets revoked_at — the mechanism behind Settings ->
// Members' revoke button (task 4.5 builds that page; this is its engine).
func RevokeInvitation(ctx context.Context, db *sqlx.DB, invitationID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		UPDATE invitations SET revoked_at = now()
		WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, invitationID)
	return err
}

// ResendInvitation revokes the old token and creates a fresh one with a
// new expiry, in one transaction — "kirim ulang mutar token dan mbatalin
// yang lama" (task 1.7). Returns the new invitation's id, for the caller
// to build the new link around a token it generated itself (this function
// never sees the raw token, only its hash, same as CreateInvitation).
func ResendInvitation(ctx context.Context, db *sqlx.DB, oldInvitationID uuid.UUID, newTokenHash string, newExpiresAt time.Time) (uuid.UUID, error) {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var orgID, roleID, invitedBy uuid.UUID
	var email string
	err = tx.QueryRowxContext(ctx, `
		UPDATE invitations SET revoked_at = now()
		WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND declined_at IS NULL
		RETURNING org_id, email, role_id, invited_by`, oldInvitationID).
		Scan(&orgID, &email, &roleID, &invitedBy)
	if err != nil {
		return uuid.Nil, err
	}

	var newID uuid.UUID
	err = tx.QueryRowxContext(ctx, `
		INSERT INTO invitations (org_id, email, role_id, invited_by, token_hash, expires_at, project_ids)
		SELECT $1, $2, $3, $4, $5, $6, project_ids FROM invitations WHERE id = $7
		RETURNING id`, orgID, email, roleID, invitedBy, newTokenHash, newExpiresAt, oldInvitationID).Scan(&newID)
	if err != nil {
		return uuid.Nil, err
	}
	return newID, tx.Commit()
}

// ListPendingInvitationsForOrg lists org's still-usable invitations
// (unexpired, unaccepted, undeclined, unrevoked) — Settings -> Members'
// pending-invites panel, resend/revoke's own list view.
func ListPendingInvitationsForOrg(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) ([]Invitation, error) {
	var out []Invitation
	err := sqlx.SelectContext(ctx, db, &out, `
		SELECT i.id, i.org_id, i.email, i.role_id, r.key AS role_key, i.invited_by,
		       i.expires_at, i.accepted_at, i.revoked_at, i.declined_at
		FROM invitations i
		JOIN roles r ON r.id = i.role_id
		WHERE i.org_id = $1
		  AND i.expires_at > now()
		  AND i.accepted_at IS NULL
		  AND i.revoked_at IS NULL
		  AND i.declined_at IS NULL
		ORDER BY i.expires_at`, orgID)
	return out, err
}

// CountRecentInvitationsByOrg supports task 1.7's per-organization rate
// limit — invite spam is a real abuse vector against the sending domain's
// reputation, not a hypothetical.
func CountRecentInvitationsByOrg(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, db, &n,
		`SELECT count(*) FROM invitations WHERE org_id = $1 AND created_at > $2`, orgID, since)
	return n, err
}

// CountRecentInvitationsByEmail supports the per-destination rate limit —
// bounds how many times one address can be re-invited (e.g. via resend) in
// a window, independent of which org is doing the inviting.
func CountRecentInvitationsByEmail(ctx context.Context, db *sqlx.DB, email string, since time.Time) (int, error) {
	var n int
	err := sqlx.GetContext(ctx, db, &n,
		`SELECT count(*) FROM invitations WHERE email = $1 AND created_at > $2`, email, since)
	return n, err
}
