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

// OrgProfile is the fuller row /settings/organization reads and writes —
// separate from Org (above) rather than adding fields to it, since Org is
// deliberately the minimal shape the hot auth/resolver path needs on every
// request and this one carries columns nothing on that path touches.
type OrgProfile struct {
	ID                         uuid.UUID `db:"id"`
	Subdomain                  string    `db:"subdomain"`
	Name                       string    `db:"name"`
	Timezone                   string    `db:"timezone"`
	DefaultSyncIntervalSeconds *int      `db:"default_sync_interval_seconds"`
	RetentionDays              int       `db:"retention_days"`
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

// OrgProfileByID reads the settings-screen shape — live orgs only, same as
// every other "by id" read outside the resolver's own deleted-aware path.
func OrgProfileByID(ctx context.Context, db *sqlx.DB, id uuid.UUID) (*OrgProfile, error) {
	var out OrgProfile
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, subdomain, name, timezone, default_sync_interval_seconds, retention_days
		FROM organizations WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateOrgProfile writes the plain (non-subdomain, non-ownership) fields
// /settings/organization exposes. Subdomain change and ownership transfer
// each get their own function below — both are sensitive enough (a wrong
// subdomain breaks every bookmark/CI URL; a wrong owner locks someone out)
// to want their own explicit call site and audit event, not a field on a
// general-purpose update.
func UpdateOrgProfile(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, name, timezone string, defaultSyncIntervalSeconds *int, retentionDays int) error {
	_, err := db.ExecContext(ctx, `
		UPDATE organizations
		SET name = $2, timezone = $3, default_sync_interval_seconds = $4, retention_days = $5
		WHERE id = $1 AND deleted_at IS NULL`,
		orgID, name, timezone, defaultSyncIntervalSeconds, retentionDays)
	return err
}

// UpdateOrgSubdomain is Owner-only at the handler layer (authz's
// org.slug.change permission) — this function only enforces the same
// uniqueness ErrSlugTaken already gives CreateOrgWithOwner, via the
// column's own UNIQUE constraint. Reserved/retired-slug checks are the
// caller's job (internal/web), mirroring task 1.6's onboarding validation
// exactly, since both are "is this slug choosable" checks against the same
// two lookup tables and deserve one shared code path, not two.
func UpdateOrgSubdomain(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, newSubdomain string) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var oldSubdomain string
	if err := tx.GetContext(ctx, &oldSubdomain, `
		SELECT subdomain FROM organizations WHERE id = $1 AND deleted_at IS NULL`, orgID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE organizations SET subdomain = $2 WHERE id = $1`, orgID, newSubdomain); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSlugTaken
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO retired_slugs (slug, org_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, oldSubdomain, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// TransferOwnership promotes toUserID to Owner and demotes fromUserID to
// Admin, in that order within one transaction — promoting first means the
// org always has at least one active Owner at every intermediate point,
// so task 0.2's >=1-Owner trigger never sees a zero-Owner state to reject,
// even though it's only demoting one specific membership, not removing it.
func TransferOwnership(ctx context.Context, db *sqlx.DB, orgID, fromUserID, toUserID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var ownerRoleID, adminRoleID uuid.UUID
	if err := tx.GetContext(ctx, &ownerRoleID, `SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		return err
	}
	if err := tx.GetContext(ctx, &adminRoleID, `SELECT id FROM roles WHERE key = 'admin' AND org_id IS NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE memberships SET role_id = $1 WHERE org_id = $2 AND user_id = $3 AND status = 'active'`,
		ownerRoleID, orgID, toUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE memberships SET role_id = $1 WHERE org_id = $2 AND user_id = $3 AND status = 'active'`,
		adminRoleID, orgID, fromUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE organizations SET perm_epoch = perm_epoch + 1 WHERE id = $1`, orgID); err != nil {
		return err
	}
	return tx.Commit()
}

// SoftDeleteOrganization is the danger-zone "hapus organization" action —
// sets deleted_at (the subdomain resolver already treats that as 404, same
// as any other deleted org) and retires the slug so it's never reassigned,
// the same tombstone UpdateOrgSubdomain leaves behind for its old slug.
func SoftDeleteOrganization(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var subdomain string
	if err := tx.QueryRowxContext(ctx, `
		UPDATE organizations SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL
		RETURNING subdomain`, orgID).Scan(&subdomain); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO retired_slugs (slug, org_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, subdomain, orgID); err != nil {
		return err
	}
	return tx.Commit()
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
