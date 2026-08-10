// Package authz implements RBAC (task 1.4): a single Principal type shared
// by every auth path (session today, API tokens once task 1.8 lands), a
// declarative Require(perm) route guard, and the fixed 4-role permission
// matrix from the spec.
package authz

import (
	"slices"

	"github.com/google/uuid"
)

// Kind distinguishes how a Principal was established. Both kinds resolve
// to the same struct shape specifically so a handler never has to know or
// care which one it's dealing with — the bug this closes is documented in
// the old codebase: the token auth path there never populated the same
// context value the cookie path did, so every permission check downstream
// had to special-case it.
type Kind int

const (
	KindSession Kind = iota
	KindToken
)

// Principal is who is making this request and what they're allowed to do.
// TokenScopes is nil for a session-based Principal; task 1.8 populates it
// for token-based ones. UserID is uuid.Nil for an organization service
// token (task 1.8) — it has no owning user to derive a role from, so its
// permission is TokenScopes alone; a Personal Access Token has UserID set
// and Role is the intersection point: resolved fresh per request from the
// owner's *current* role, never cached into the token itself.
//
// PinnedProjectID/PinnedComponentID are set only for a service token
// created with a pin (task 1.8): the endpoint that eventually consumes
// this Principal (task 3.4's ingest endpoint) is responsible for checking
// a requested resource against these — Principal itself has no notion of
// "which resource this request is about."
type Principal struct {
	Kind              Kind
	UserID            uuid.UUID
	OrgID             uuid.UUID
	Role              string
	TokenScopes       []string
	PinnedProjectID   uuid.UUID
	PinnedComponentID uuid.UUID
}

// Can reports whether this principal is allowed perm.
//   - Session: perm must be in the role's permission set.
//   - PAT (Kind==KindToken, UserID set): perm must be in both the token's
//     own scopes and its owner's current role — a token can narrow its
//     owner's access but never exceed it.
//   - Service token (Kind==KindToken, UserID unset): perm must be in the
//     token's own scopes; there is no owning role to intersect with.
func (p Principal) Can(perm string) bool {
	if p.Kind != KindToken {
		return rolePermissions[p.Role][perm]
	}
	if !slices.Contains(p.TokenScopes, perm) {
		return false
	}
	if p.UserID == uuid.Nil {
		return true
	}
	return rolePermissions[p.Role][perm]
}

// rolePermissions is the fixed 4-role matrix from task 1.4. Custom
// per-organization roles are schema-ready (roles.org_id, task 0.2) but
// have no UI in v1, so a static map is the whole story for now — the
// moment custom roles ship, this needs to become a DB-backed lookup keyed
// by role_id instead of role key.
var rolePermissions = map[string]map[string]bool{
	"owner": setOf(
		"dashboard.read", "project.create", "project.update", "project.delete",
		"component.create", "component.update", "component.delete",
		"metric.bind", "metric.unbind",
		"connection.read", "connection.secret.write",
		"sync.trigger", "sync.schedule",
		"member.invite", "member.remove", "member.role.change",
		"org.settings.update", "org.domain.verify", "org.slug.change", "org.sso.configure",
		"org.delete", "org.transfer_ownership",
		"token.manage.own", "token.manage.org", "audit.read",
	),
	"admin": setOf(
		"dashboard.read", "project.create", "project.update", "project.delete",
		"component.create", "component.update", "component.delete",
		"metric.bind", "metric.unbind",
		"connection.read", "connection.secret.write",
		"sync.trigger", "sync.schedule",
		"member.invite", "member.remove", "member.role.change",
		"org.settings.update", "org.domain.verify",
		"token.manage.own", "token.manage.org", "audit.read",
	),
	"member": setOf(
		"dashboard.read", "project.create", "project.update",
		"component.create", "component.update",
		"metric.bind", "metric.unbind",
		"connection.read",
		"sync.trigger",
		"token.manage.own",
	),
	"viewer": setOf(
		"dashboard.read",
		"token.manage.own",
	),
}

func setOf(perms ...string) map[string]bool {
	out := make(map[string]bool, len(perms))
	for _, p := range perms {
		out[p] = true
	}
	return out
}

// CanChangeRole implements the Owner-promotion/demotion guard from task
// 1.4: an Admin can change roles among Member/Viewer/Admin, but can never
// touch anyone currently Owner, and can never promote anyone to Owner.
// Only an Owner can create or remove another Owner.
func CanChangeRole(actorRole, targetCurrentRole, newRole string) bool {
	if actorRole == "owner" {
		return true
	}
	if actorRole != "admin" {
		return false
	}
	return targetCurrentRole != "owner" && newRole != "owner"
}
