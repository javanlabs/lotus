// Package repo holds every tenant-scoped database query in Lotus.
//
// Every exported function here takes a tenant.Scope as its first argument
// — never a bare *sqlx.DB or *sqlx.Tx — so a query against a tenant table
// can't exist without an organization already bound to it:
//
//	func (r *ProjectRepo) BySlug(s tenant.Scope, slug string) (*Project, error)
//
// The one deliberate exception is internal/repo/global, for the
// control-plane tables (task 0.2) that are genuinely cross-organization by
// design. Nothing else may call db.Get/db.Select/db.Exec/db.Query directly
// — enforced by scripts/check-repo-scope.sh in CI, since Go's type system
// alone can't express "only these two packages may hold a raw connection."
package repo
