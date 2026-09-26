package authz_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
)

// permissionMatrix mirrors task 1.4's spec table exactly, so this test
// fails the moment the implementation drifts from it.
var permissionMatrix = map[string][]string{
	"dashboard.read":          {"owner", "admin", "member", "viewer"},
	"project.create":          {"owner", "admin", "member"},
	"project.update":          {"owner", "admin", "member"},
	"project.delete":          {"owner", "admin"},
	"folder.create":           {"owner", "admin", "member"},
	"folder.update":           {"owner", "admin", "member"},
	"folder.delete":           {"owner", "admin"},
	"component.create":        {"owner", "admin", "member"},
	"component.update":        {"owner", "admin", "member"},
	"component.delete":        {"owner", "admin"},
	"metric.bind":             {"owner", "admin", "member"},
	"metric.unbind":           {"owner", "admin", "member"},
	"connection.read":         {"owner", "admin", "member"},
	"connection.secret.write": {"owner", "admin"},
	"sync.trigger":            {"owner", "admin", "member"},
	"sync.schedule":           {"owner", "admin"},
	"metrics.ingest":          {"owner", "admin", "member"},
	"member.invite":           {"owner", "admin"},
	"member.remove":           {"owner", "admin"},
	"member.role.change":      {"owner", "admin"},
	"org.settings.update":     {"owner", "admin"},
	"org.domain.verify":       {"owner", "admin"},
	"org.slug.change":         {"owner"},
	"org.sso.configure":       {"owner"},
	"org.delete":              {"owner"},
	"org.transfer_ownership":  {"owner"},
	"token.manage.own":        {"owner", "admin", "member", "viewer"},
	"token.manage.org":        {"owner", "admin"},
	"audit.read":              {"owner", "admin"},
}

var allRoles = []string{"owner", "admin", "member", "viewer"}

func TestPermissionMatrix(t *testing.T) {
	for perm, allowedRoles := range permissionMatrix {
		allowed := make(map[string]bool, len(allowedRoles))
		for _, r := range allowedRoles {
			allowed[r] = true
		}
		for _, role := range allRoles {
			p := authz.Principal{Kind: authz.KindSession, Role: role}
			got := p.Can(perm)
			want := allowed[role]
			if got != want {
				t.Errorf("%s.Can(%q) = %v, want %v", role, perm, got, want)
			}
		}
	}
}

func TestCanChangeRole_OwnerCanDoAnything(t *testing.T) {
	if !authz.CanChangeRole("owner", "member", "owner") {
		t.Error("Owner should be able to promote to Owner")
	}
	if !authz.CanChangeRole("owner", "owner", "admin") {
		t.Error("Owner should be able to demote another Owner")
	}
}

func TestCanChangeRole_AdminCannotTouchOwner(t *testing.T) {
	if authz.CanChangeRole("admin", "owner", "admin") {
		t.Error("Admin must not be able to demote an Owner")
	}
	if authz.CanChangeRole("admin", "member", "owner") {
		t.Error("Admin must not be able to promote anyone to Owner")
	}
	if !authz.CanChangeRole("admin", "member", "admin") {
		t.Error("Admin should be able to promote Member to Admin")
	}
}

func TestCanChangeRole_MemberAndViewerCannotChangeRoles(t *testing.T) {
	if authz.CanChangeRole("member", "viewer", "member") {
		t.Error("Member must not be able to change roles")
	}
	if authz.CanChangeRole("viewer", "viewer", "member") {
		t.Error("Viewer must not be able to change roles")
	}
}

func TestTokenPrincipal_ScopedByBothRoleAndTokenScopes(t *testing.T) {
	// A Personal Access Token (task 1.8): UserID set means its
	// permission is the intersection of its own scopes and its owner's
	// current role.
	p := authz.Principal{
		Kind:        authz.KindToken,
		UserID:      uuid.New(),
		Role:        "viewer", // viewer's role alone doesn't grant sync.trigger
		TokenScopes: []string{"dashboard.read", "sync.trigger"},
	}
	// Even though the token itself claims sync.trigger scope, the owner's
	// viewer role doesn't have it — a token can never exceed its owner.
	if p.Can("sync.trigger") {
		t.Error("token must not exceed its owner's role permissions")
	}
	if !p.Can("dashboard.read") {
		t.Error("token should be able to use a scope its role also grants")
	}

	p2 := authz.Principal{
		Kind:        authz.KindToken,
		UserID:      uuid.New(),
		Role:        "admin", // admin's role grants sync.trigger
		TokenScopes: []string{"dashboard.read"},
	}
	// Role allows it, but the token itself was never scoped for it.
	if p2.Can("sync.trigger") {
		t.Error("token must not exceed its own declared scopes")
	}
}

func TestServiceTokenPrincipal_ScopedByTokenScopesAlone(t *testing.T) {
	// A service token (task 1.8): UserID unset (uuid.Nil) means there is
	// no owning role to intersect with — its own scopes are the whole
	// story.
	p := authz.Principal{
		Kind:        authz.KindToken,
		TokenScopes: []string{"metrics:write"},
	}
	if !p.Can("metrics:write") {
		t.Error("service token should be able to use its own declared scope")
	}
	if p.Can("metrics:read") {
		t.Error("service token must not exceed its own declared scopes")
	}
}
