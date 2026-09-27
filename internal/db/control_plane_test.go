package db_test

import (
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
)

func TestMigrateUpIsIdempotent(t *testing.T) {
	dsn := dbtest.DSN(t)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
}

func TestSeedsFiveSystemRoles(t *testing.T) {
	sqlDB := dbtest.Open(t)

	var keys []string
	err := sqlDB.Select(&keys, `SELECT key FROM roles WHERE org_id IS NULL ORDER BY key`)
	if err != nil {
		t.Fatalf("select roles: %v", err)
	}
	want := []string{"admin", "guest", "member", "owner", "viewer"}
	if len(keys) != len(want) {
		t.Fatalf("roles = %v, want %v", keys, want)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Fatalf("roles = %v, want %v", keys, want)
		}
	}
}

func TestSeedsReservedSubdomains(t *testing.T) {
	sqlDB := dbtest.Open(t)

	for _, name := range []string{"www", "auth", "healthz", ".well-known"} {
		var exists bool
		if err := sqlDB.Get(&exists, `SELECT EXISTS (SELECT 1 FROM reserved_subdomains WHERE name = $1)`, name); err != nil {
			t.Fatalf("check %s: %v", name, err)
		}
		if !exists {
			t.Fatalf("reserved_subdomains missing %q", name)
		}
	}
}

func TestSubdomainCheckConstraint(t *testing.T) {
	sqlDB := dbtest.Open(t)

	cases := []struct {
		subdomain string
		ok        bool
	}{
		{"-abc", false},
		{"abc-", false},
		{"a--b", false},
		{"xn--foo", false},
		{"ABC", false},
		{"", false},
		{"javan", true},
		{"acme-corp", true},
		{"a1", true},
	}
	for _, c := range cases {
		_, err := sqlDB.Exec(`INSERT INTO organizations (subdomain, name) VALUES ($1, 'x')`, c.subdomain)
		if c.ok && err != nil {
			t.Errorf("subdomain %q: expected accept, got error: %v", c.subdomain, err)
		}
		if !c.ok && err == nil {
			t.Errorf("subdomain %q: expected rejection, but insert succeeded", c.subdomain)
		}
	}
}

func TestUniqueProviderIdentity(t *testing.T) {
	sqlDB := dbtest.Open(t)

	var userA, userB string
	if err := sqlDB.Get(&userA, `INSERT INTO users (email) VALUES ('a@example.com') RETURNING id`); err != nil {
		t.Fatalf("insert user A: %v", err)
	}
	if err := sqlDB.Get(&userB, `INSERT INTO users (email) VALUES ('b@example.com') RETURNING id`); err != nil {
		t.Fatalf("insert user B: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO user_identities (user_id, provider, provider_sub) VALUES ($1, 'github', 'sub-1')`, userA); err != nil {
		t.Fatalf("insert identity for A: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO user_identities (user_id, provider, provider_sub) VALUES ($1, 'github', 'sub-1')`, userB); err == nil {
		t.Fatal("expected duplicate (provider, provider_sub) to be rejected")
	}
}

func TestMinOneOwnerInvariant(t *testing.T) {
	sqlDB := dbtest.Open(t)

	orgID := mustInsertOrg(t, sqlDB, "acme")
	ownerRoleID := mustRoleID(t, sqlDB, "owner")
	adminRoleID := mustRoleID(t, sqlDB, "admin")

	ownerUserID := mustInsertUser(t, sqlDB, "owner@acme.com")
	membershipID := mustInsertMembership(t, sqlDB, orgID, ownerUserID, ownerRoleID)

	// Demoting the sole Owner is rejected.
	_, err := sqlDB.Exec(`UPDATE memberships SET role_id = $1 WHERE id = $2`, adminRoleID, membershipID)
	if err == nil {
		t.Fatal("expected demoting the last Owner to be rejected")
	}

	// Deleting the sole Owner's membership is rejected.
	_, err = sqlDB.Exec(`DELETE FROM memberships WHERE id = $1`, membershipID)
	if err == nil {
		t.Fatal("expected deleting the last Owner to be rejected")
	}

	// Adding a second Owner, then demoting the first, is allowed.
	secondOwnerUserID := mustInsertUser(t, sqlDB, "owner2@acme.com")
	mustInsertMembership(t, sqlDB, orgID, secondOwnerUserID, ownerRoleID)

	if _, err := sqlDB.Exec(`UPDATE memberships SET role_id = $1 WHERE id = $2`, adminRoleID, membershipID); err != nil {
		t.Fatalf("expected demote to succeed with a second Owner present: %v", err)
	}
}

func mustInsertOrg(t *testing.T, sqlDB *sqlx.DB, subdomain string) string {
	t.Helper()
	var id string
	if err := sqlDB.Get(&id, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org %s: %v", subdomain, err)
	}
	return id
}

func mustInsertUser(t *testing.T, sqlDB *sqlx.DB, email string) string {
	t.Helper()
	var id string
	if err := sqlDB.Get(&id, `INSERT INTO users (email) VALUES ($1) RETURNING id`, email); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func mustRoleID(t *testing.T, sqlDB *sqlx.DB, key string) string {
	t.Helper()
	var id string
	if err := sqlDB.Get(&id, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, key); err != nil {
		t.Fatalf("lookup role %s: %v", key, err)
	}
	return id
}

func mustInsertMembership(t *testing.T, sqlDB *sqlx.DB, orgID, userID, roleID string) string {
	t.Helper()
	var id string
	err := sqlDB.Get(&id, `INSERT INTO memberships (org_id, user_id, role_id) VALUES ($1, $2, $3) RETURNING id`, orgID, userID, roleID)
	if err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return id
}
