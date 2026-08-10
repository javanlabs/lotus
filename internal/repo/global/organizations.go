package global

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Org is the subset of the organizations row the subdomain resolver
// (task 0.5) and session/auth code need. It is not the full control-plane
// model — just enough to resolve a Host and build a tenant.Scope.
type Org struct {
	ID        uuid.UUID  `db:"id"`
	Subdomain string     `db:"subdomain"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at"`
}

// OrgBySubdomain looks up an organization by its subdomain label. Returns
// sql.ErrNoRows (wrapped) when no organization has that subdomain —
// callers must treat that the same as "not found", not as an error worth
// surfacing, since an unclaimed subdomain is an expected, common case.
func OrgBySubdomain(ctx context.Context, db *sqlx.DB, subdomain string) (*Org, error) {
	var org Org
	err := sqlx.GetContext(ctx, db, &org,
		`SELECT id, subdomain, name, deleted_at FROM organizations WHERE subdomain = $1`, subdomain)
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
