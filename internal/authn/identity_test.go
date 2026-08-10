package authn_test

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/dbtest"
)

func TestResolveUser_SameVerifiedEmailAcrossProviders_LinksOneUser(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	u1, outcome1, err := authn.ResolveUser(ctx, db, "github", authn.Identity{
		ProviderSub: "gh-1", Email: "alice@acme.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("github resolve: %v", err)
	}
	if outcome1 != authn.OutcomeNewUser {
		t.Fatalf("first login outcome = %v, want OutcomeNewUser", outcome1)
	}

	u2, outcome2, err := authn.ResolveUser(ctx, db, "google", authn.Identity{
		ProviderSub: "gg-1", Email: "alice@acme.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("google resolve: %v", err)
	}
	if outcome2 != authn.OutcomeLinked {
		t.Fatalf("second login outcome = %v, want OutcomeLinked", outcome2)
	}
	if u1.ID != u2.ID {
		t.Fatalf("expected one user row, got two: %s != %s", u1.ID, u2.ID)
	}

	assertUserCount(t, db, 1)
	assertIdentityCount(t, db, 2)
}

func TestResolveUser_GitHubNoreplyEmail_NewUnlinkedUser(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	// GitHubProvider.FetchIdentity already filters out unverified/noreply
	// emails before this point — an Identity with EmailVerified: false and
	// no email is exactly what it hands back for a "no primary verified
	// email" GitHub account. ResolveUser must not try to match anyone.
	user, outcome, err := authn.ResolveUser(ctx, db, "github", authn.Identity{
		ProviderSub: "gh-2", Email: "", EmailVerified: false,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if outcome != authn.OutcomeNewUser {
		t.Fatalf("outcome = %v, want OutcomeNewUser", outcome)
	}
	if user.Email != nil {
		t.Fatalf("expected no email on file, got %q", *user.Email)
	}
}

func TestResolveUser_MatchingEmailButUnverifiedAtProvider_SeparateUser(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	verified, _, err := authn.ResolveUser(ctx, db, "google", authn.Identity{
		ProviderSub: "gg-victim", Email: "alice@corp.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("seed verified user: %v", err)
	}

	// A second identity claims the same email, but this provider says it's
	// NOT verified — must not auto-link, even though a verified match
	// exists.
	attacker, outcome, err := authn.ResolveUser(ctx, db, "github", authn.Identity{
		ProviderSub: "gh-attacker", Email: "alice@corp.com", EmailVerified: false,
	})
	if err != nil {
		t.Fatalf("resolve unverified claim: %v", err)
	}
	if outcome != authn.OutcomeNewUser {
		t.Fatalf("outcome = %v, want OutcomeNewUser (must not auto-link)", outcome)
	}
	if attacker.ID == verified.ID {
		t.Fatal("unverified email claim must not be merged into the existing verified account")
	}
	if attacker.Email != nil {
		t.Fatalf("unverified email must not be persisted, got %q", *attacker.Email)
	}
}

func TestResolveUser_EmailNormalization(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	u1, _, err := authn.ResolveUser(ctx, db, "google", authn.Identity{
		ProviderSub: "gg-a", Email: "alice@acme.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	// Same email, different case -> same account (case-insensitive match).
	u2, outcome, err := authn.ResolveUser(ctx, db, "github", authn.Identity{
		ProviderSub: "gh-a", Email: "Alice@Acme.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if outcome != authn.OutcomeLinked || u1.ID != u2.ID {
		t.Fatalf("Alice@Acme.com should link to alice@acme.com's account")
	}

	// Dot-stripping is Gmail-specific and must NOT be applied — these are
	// different accounts.
	u3, _, err := authn.ResolveUser(ctx, db, "google", authn.Identity{
		ProviderSub: "gg-b", Email: "alice@gmail.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("resolve 3: %v", err)
	}
	u4, outcome4, err := authn.ResolveUser(ctx, db, "github", authn.Identity{
		ProviderSub: "gh-b", Email: "a.lice@gmail.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("resolve 4: %v", err)
	}
	if outcome4 == authn.OutcomeLinked || u3.ID == u4.ID {
		t.Fatal("a.lice@gmail.com must be treated as different from alice@gmail.com")
	}
}

func assertUserCount(t *testing.T, db *sqlx.DB, want int) {
	t.Helper()
	var got int
	if err := db.Get(&got, `SELECT count(*) FROM users`); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if got != want {
		t.Fatalf("users count = %d, want %d", got, want)
	}
}

func assertIdentityCount(t *testing.T, db *sqlx.DB, want int) {
	t.Helper()
	var got int
	if err := db.Get(&got, `SELECT count(*) FROM user_identities`); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if got != want {
		t.Fatalf("identities count = %d, want %d", got, want)
	}
}
