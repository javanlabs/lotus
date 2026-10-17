package authn_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

// seedBootstrap mirrors what `lotus init` does: an organization with zero
// memberships plus a pending bootstrap row. One dbtest.Open per test — each
// call gets its own schema, so the db must be threaded through, not reopened.
func seedBootstrap(t *testing.T, ownerEmail string) (*sqlx.DB, uuid.UUID) {
	t.Helper()
	db := dbtest.Open(t)
	var orgID uuid.UUID
	if err := db.Get(&orgID,
		`INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := global.CreateBootstrap(context.Background(), db, orgID, ownerEmail); err != nil {
		t.Fatalf("CreateBootstrap: %v", err)
	}
	return db, orgID
}

func claimAs(t *testing.T, db *sqlx.DB, email string, verified bool) (bool, error) {
	t.Helper()
	var userID uuid.UUID
	if err := db.Get(&userID,
		`INSERT INTO users (email, name, email_verified_at) VALUES ($1, 'T', now()) RETURNING id`, email); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return authn.ClaimOwnershipIfPending(context.Background(), db, userID,
		authn.Identity{Email: email, EmailVerified: verified})
}

func ownerCount(t *testing.T, db *sqlx.DB, orgID uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Get(&n,
		`SELECT count(*) FROM memberships m JOIN roles r ON r.id = m.role_id
		  WHERE m.org_id = $1 AND r.key = 'owner' AND m.status = 'active'`, orgID); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	return n
}

func TestClaimOwnership_MatchingVerifiedEmailBecomesOwner(t *testing.T) {
	t.Parallel()
	db, orgID := seedBootstrap(t, "you@javan.co.id")
	claimed, err := claimAs(t, db, "you@javan.co.id", true)
	if err != nil || !claimed {
		t.Fatalf("claim = %v, err = %v; want true, nil", claimed, err)
	}
	if n := ownerCount(t, db, orgID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}

// TestClaimOwnership_UnverifiedEmailIsRejected is the security case: an IdP
// that lets a user self-assert an arbitrary address must not be able to
// hand them ownership of a fresh instance.
func TestClaimOwnership_UnverifiedEmailIsRejected(t *testing.T) {
	t.Parallel()
	db, orgID := seedBootstrap(t, "you@javan.co.id")
	if _, err := claimAs(t, db, "you@javan.co.id", false); err == nil {
		t.Fatal("claim with an unverified email returned nil error, want rejection")
	}
	if n := ownerCount(t, db, orgID); n != 0 {
		t.Fatalf("owner count = %d, want 0", n)
	}
}

func TestClaimOwnership_NonMatchingEmailIsRejected(t *testing.T) {
	t.Parallel()
	db, orgID := seedBootstrap(t, "you@javan.co.id")
	if _, err := claimAs(t, db, "someoneelse@example.com", true); err == nil {
		t.Fatal("claim with a non-matching email returned nil error, want rejection")
	}
	if n := ownerCount(t, db, orgID); n != 0 {
		t.Fatalf("owner count = %d, want 0", n)
	}
}

// TestClaimOwnership_MatchIsCaseInsensitive: email addresses are not
// case-sensitive in practice, and an operator typing --owner-email at a
// shell should not have to match their IdP's casing exactly.
func TestClaimOwnership_MatchIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	db, orgID := seedBootstrap(t, "You@Javan.co.id")
	if claimed, err := claimAs(t, db, "you@javan.co.id", true); err != nil || !claimed {
		t.Fatalf("claim = %v, err = %v; want true, nil", claimed, err)
	}
	if n := ownerCount(t, db, orgID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}

// TestClaimOwnership_ConsumedBootstrapIsInert: once ownership is claimed, a
// later login must not add a second Owner or re-run the claim.
func TestClaimOwnership_ConsumedBootstrapIsInert(t *testing.T) {
	t.Parallel()
	db, orgID := seedBootstrap(t, "you@javan.co.id")
	if _, err := claimAs(t, db, "you@javan.co.id", true); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	claimed, err := claimAs(t, db, "later@javan.co.id", true)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("claimed = true on an already-consumed bootstrap, want false")
	}
	if n := ownerCount(t, db, orgID); n != 1 {
		t.Fatalf("owner count = %d, want 1", n)
	}
}
