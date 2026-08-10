package global

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// ErrSlugTaken is returned by CreateOrgWithOwner when the subdomain is
// already in use — the availability check (task 1.6) is advisory only;
// this is the actual authority, so two concurrent signups for the same
// slug always resolve to exactly one winner, not a race that occasionally
// lets both through.
var ErrSlugTaken = errors.New("subdomain already taken")

// CreateOrgWithOwner creates an organization and makes ownerUserID its
// first (and, per the >=1-Owner invariant, permanent-until-someone-else-
// joins) Owner, in one transaction. Returns ErrSlugTaken if subdomain's
// UNIQUE constraint rejects the insert.
func CreateOrgWithOwner(ctx context.Context, db *sqlx.DB, subdomain, name string, ownerUserID uuid.UUID) (uuid.UUID, error) {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var orgID uuid.UUID
	err = tx.GetContext(ctx, &orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $2) RETURNING id`, subdomain, name)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return uuid.Nil, ErrSlugTaken
		}
		return uuid.Nil, err
	}

	var ownerRoleID uuid.UUID
	if err := tx.GetContext(ctx, &ownerRoleID, `SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, ownerUserID, ownerRoleID); err != nil {
		return uuid.Nil, err
	}

	return orgID, tx.Commit()
}

// Org is the subset of the organizations row the subdomain resolver
// (task 0.5) and session/auth code need. It is not the full control-plane
// model — just enough to resolve a Host, build a tenant.Scope, and check
// session freshness against perm_epoch (task 1.3).
type Org struct {
	ID        uuid.UUID  `db:"id"`
	Subdomain string     `db:"subdomain"`
	Name      string     `db:"name"`
	PermEpoch int        `db:"perm_epoch"`
	DeletedAt *time.Time `db:"deleted_at"`
}

// OrgByID looks up an organization by id, including deleted ones — callers
// that need to distinguish "deleted" from "never existed" (unlike the
// subdomain resolver, which treats both as 404) use this directly.
func OrgByID(ctx context.Context, db *sqlx.DB, id uuid.UUID) (*Org, error) {
	var org Org
	err := sqlx.GetContext(ctx, db, &org,
		`SELECT id, subdomain, name, perm_epoch, deleted_at FROM organizations WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &org, nil
}

// BumpPermEpoch increments an organization's perm_epoch — called on every
// membership/role mutation (task 1.3). A session whose own perm_epoch
// falls behind the org's is treated as stale by RequireTenantSession, which
// re-reads membership and either rotates the session (still a member) or
// revokes it (removed) on the very next request — no re-login needed for
// a demotion to take effect, and no lingering access after removal.
func BumpPermEpoch(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `UPDATE organizations SET perm_epoch = perm_epoch + 1 WHERE id = $1`, orgID)
	return err
}

// OrgBySubdomain looks up an organization by its subdomain label. Returns
// sql.ErrNoRows (wrapped) when no organization has that subdomain —
// callers must treat that the same as "not found", not as an error worth
// surfacing, since an unclaimed subdomain is an expected, common case.
func OrgBySubdomain(ctx context.Context, db *sqlx.DB, subdomain string) (*Org, error) {
	var org Org
	err := sqlx.GetContext(ctx, db, &org,
		`SELECT id, subdomain, name, perm_epoch, deleted_at FROM organizations WHERE subdomain = $1`, subdomain)
	if err != nil {
		return nil, err
	}
	return &org, nil
}

// IsReservedSubdomain reports whether name is in reserved_subdomains.
func IsReservedSubdomain(ctx context.Context, db *sqlx.DB, name string) (bool, error) {
	var exists bool
	err := sqlx.GetContext(ctx, db, &exists,
		`SELECT EXISTS (SELECT 1 FROM reserved_subdomains WHERE name = $1)`, name)
	return exists, err
}

// IsRetiredSlug reports whether slug belongs to a deleted organization.
// Retired slugs are never reassigned — see internal/tenant's resolver.
func IsRetiredSlug(ctx context.Context, db *sqlx.DB, slug string) (bool, error) {
	var exists bool
	err := sqlx.GetContext(ctx, db, &exists,
		`SELECT EXISTS (SELECT 1 FROM retired_slugs WHERE slug = $1)`, slug)
	return exists, err
}
