package global

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// ErrNoOrg means the database has no live organization. In single mode
// that is the state between `lotus migrate` and `lotus init` — not a
// failure worth a 500, so the resolver turns it into a 404.
var ErrNoOrg = errors.New("global: no organization exists")

// TheOnlyOrg returns the single organization a single-mode install holds.
// LIMIT 2 rather than LIMIT 1 on purpose: if a second organization ever
// appears in a single-mode database that is a real invariant violation and
// must be loud, not silently resolved to whichever row sorted first.
func TheOnlyOrg(ctx context.Context, db *sqlx.DB) (*Org, error) {
	var orgs []Org
	err := db.SelectContext(ctx, &orgs,
		`SELECT id, subdomain, name, perm_epoch, deleted_at
		   FROM organizations
		  WHERE deleted_at IS NULL
		  ORDER BY created_at
		  LIMIT 2`)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	switch len(orgs) {
	case 0:
		return nil, ErrNoOrg
	case 1:
		return &orgs[0], nil
	default:
		return nil, errors.New("global: single mode requires exactly one organization, found more than one")
	}
}

// Bootstrap is a pending single-mode ownership claim, written by
// `lotus init` and consumed by the first matching verified login.
type Bootstrap struct {
	OrgID      uuid.UUID  `db:"org_id"`
	OwnerEmail string     `db:"owner_email"`
	ConsumedAt *time.Time `db:"consumed_at"`
}

// ErrBootstrapExists means `lotus init` has already run against this
// database. A single-mode install holds exactly one organization.
var ErrBootstrapExists = errors.New("global: this instance is already initialized")

func CreateBootstrap(ctx context.Context, db *sqlx.DB, orgID uuid.UUID, ownerEmail string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO instance_bootstrap (org_id, owner_email) VALUES ($1, $2)`, orgID, ownerEmail)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrBootstrapExists
	}
	return err
}

// PendingBootstrap returns the unconsumed bootstrap row, or nil when there
// is none — either because `lotus init` never ran or because ownership has
// already been claimed.
func PendingBootstrap(ctx context.Context, db *sqlx.DB) (*Bootstrap, error) {
	var b Bootstrap
	err := db.GetContext(ctx, &b,
		`SELECT org_id, owner_email, consumed_at FROM instance_bootstrap WHERE consumed_at IS NULL`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ConsumeBootstrap inserts the Owner membership and marks the bootstrap
// consumed in one transaction, so a crash between the two cannot leave an
// instance that is both owner-less and no longer claimable. The SELECT ...
// FOR UPDATE serializes two simultaneous first-logins: the loser finds no
// unconsumed row and claims nothing.
func ConsumeBootstrap(ctx context.Context, db *sqlx.DB, userID uuid.UUID) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var b Bootstrap
	err = tx.GetContext(ctx, &b,
		`SELECT org_id, owner_email, consumed_at FROM instance_bootstrap
		  WHERE consumed_at IS NULL FOR UPDATE`)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBootstrapExists
	}
	if err != nil {
		return err
	}

	var ownerRoleID uuid.UUID
	if err := tx.GetContext(ctx, &ownerRoleID,
		`SELECT id FROM roles WHERE key = 'owner' AND org_id IS NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3)`,
		b.OrgID, userID, ownerRoleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE instance_bootstrap SET consumed_at = now(), consumed_by = $1 WHERE consumed_at IS NULL`,
		userID); err != nil {
		return err
	}
	return tx.Commit()
}
