// Package tenant resolves which organization a request belongs to and hands
// out the only handle ("Scope") that internal/repo will accept for a query.
//
// This is the guardrail from task 0.4: RLS (task 0.3) is the fail-safe net,
// but the leak that actually happens in practice isn't forgotten
// middleware — it's a handler querying "WHERE image = $1" without "AND
// org_id = $2". Making tenant.Scope's constructor unexported closes that
// off structurally: a handler simply has no way to open a database
// transaction that isn't already bound to an organization.
package tenant

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Scope pairs a database handle with the organization it's allowed to
// touch. Constructible only from within this package — by FromContext
// (task 0.5's resolver middleware) or FromRun (the scheduler worker, task
// 3.2) — never directly by a handler, so a query path can't exist without
// an org already attached to it.
type Scope struct {
	db    *sqlx.DB
	orgID uuid.UUID
}

// newScope is the only constructor, and it is unexported: nothing outside
// this package can produce a Scope. Callers must go through FromContext or
// FromRun, both of which derive the org from something the caller doesn't
// control (the resolved Host, or the run being processed) rather than
// accepting an org id as a bare argument a handler could pass in by hand.
func newScope(db *sqlx.DB, orgID uuid.UUID) Scope {
	return Scope{db: db, orgID: orgID}
}

// OrgID returns the organization this scope is bound to.
func (s Scope) OrgID() uuid.UUID { return s.orgID }

// Tx opens a transaction and issues SET LOCAL app.org_id before handing the
// transaction to fn, so every query fn makes is subject to the org's RLS
// policy without fn needing to remember to scope it itself. SET LOCAL
// (via set_config's third argument) is transaction-scoped, not
// session-scoped, specifically so two Scopes sharing the same pooled
// connection over their lifetimes can never leak app.org_id into each
// other's queries.
func (s Scope) Tx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.org_id', $1, true)`, s.orgID.String()); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
