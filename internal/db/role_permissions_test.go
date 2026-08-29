package db_test

import (
	"slices"
	"sort"
	"testing"

	"github.com/uyab/lotus/internal/authz"

	"github.com/uyab/lotus/internal/dbtest"
)

// TestRolePermissionsSeedMatchesEveryRole is a light sanity check that the
// migration 00006 seed actually ran and covers all 4 system roles —
// guarding against the seed migration silently seeding nothing (e.g. a
// WHERE clause typo). The full matrix is compared to the Go map by
// TestRolePermissionsSeedMatchesTheGoMatrix below.
func TestRolePermissionsSeedMatchesEveryRole(t *testing.T) {
	sqlDB := dbtest.Open(t)

	var keys []string
	err := sqlDB.Select(&keys, `
		SELECT DISTINCT r.key
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.org_id IS NULL
		ORDER BY r.key`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	sort.Strings(keys)
	want := []string{"admin", "member", "owner", "viewer"}
	if len(keys) != len(want) {
		t.Fatalf("roles with seeded permissions = %v, want %v", keys, want)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Fatalf("roles with seeded permissions = %v, want %v", keys, want)
		}
	}

	var ownerCount int
	if err := sqlDB.Get(&ownerCount, `
		SELECT count(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.org_id IS NULL AND r.key = 'owner'`); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Compared to the Go matrix rather than a literal: a hardcoded number
	// here is a third copy of the same fact, and it went stale the moment
	// metrics.ingest was added.
	if want := len(authz.PermissionsForRole("owner")); ownerCount != want {
		t.Fatalf("owner permission count = %d, want %d", ownerCount, want)
	}
}

// TestRolePermissionsSeedMatchesTheGoMatrix compares the seeded table to
// the Go map row for row.
//
// Enforcement reads the Go map — role_permissions is seeded for
// documentation and for the custom-roles work that will eventually read
// it instead. Two independent copies of the same matrix with nothing
// comparing them is how they drift: the copy nobody reads goes stale, and
// whoever later switches enforcement over to the table silently changes
// what every role can do. This is the check that was missing; an earlier
// comment claimed internal/authz made it, and it did not.
func TestRolePermissionsSeedMatchesTheGoMatrix(t *testing.T) {
	sqlDB := dbtest.Open(t)

	var rows []struct {
		RoleKey    string `db:"role_key"`
		Permission string `db:"permission"`
	}
	if err := sqlDB.Select(&rows, `
		SELECT r.key AS role_key, rp.permission
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.org_id IS NULL`); err != nil {
		t.Fatalf("query: %v", err)
	}

	seeded := map[string][]string{}
	for _, row := range rows {
		seeded[row.RoleKey] = append(seeded[row.RoleKey], row.Permission)
	}

	for _, role := range []string{"owner", "admin", "member", "viewer"} {
		inGo := authz.PermissionsForRole(role)
		inDB := seeded[role]
		sort.Strings(inGo)
		sort.Strings(inDB)
		if !slices.Equal(inGo, inDB) {
			t.Errorf("role %q has drifted.\n  Go map: %v\n  seeded: %v", role, inGo, inDB)
		}
	}
}
