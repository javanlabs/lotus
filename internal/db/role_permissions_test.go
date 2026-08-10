package db_test

import (
	"sort"
	"testing"

	"github.com/uyab/lotus/internal/dbtest"
)

// TestRolePermissionsSeedMatchesEveryRole is a light sanity check that the
// migration 00006 seed actually ran and covers all 4 system roles — the
// full matrix itself is asserted against internal/authz's Go copy in
// internal/authz/principal_test.go; this just guards against the seed
// migration silently seeding nothing (e.g. a WHERE clause typo).
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
	if ownerCount != 25 {
		t.Fatalf("owner permission count = %d, want 25", ownerCount)
	}
}
