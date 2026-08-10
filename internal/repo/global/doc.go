// Package global holds the control-plane queries (task 0.2: users,
// organizations, memberships, sessions, invitations, ...) — the one
// deliberate exception to internal/repo's "every query takes a
// tenant.Scope" rule.
//
// These tables are read before Lotus knows which organization is speaking
// (resolving a Host to an org, authenticating a session, listing an
// account's memberships across every org it belongs to), so they are not
// subject to per-org Row-Level Security and have no tenant.Scope to accept.
// Functions here take a plain *sqlx.DB or *sqlx.Tx instead.
package global
