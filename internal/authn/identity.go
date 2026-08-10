// Package authn implements login on the auth host: OAuth against GitHub and
// Google (task 1.1), the handoff to a tenant host (task 1.2), and
// server-side sessions (task 1.3).
package authn

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo/global"
)

// Identity is what a Provider hands back after exchanging a code: enough
// to find-or-create a user, nothing more (no access token is ever
// persisted — see task 1.3's removal of stored provider tokens).
type Identity struct {
	ProviderSub   string
	Email         string // "" when the provider gave none (GitHub noreply/null case)
	EmailVerified bool
	Name          string // "" if the provider gave none; prefills /welcome (task 1.6)
}

// Provider is one OAuth identity provider (GitHub or Google). Every method
// takes its own base URLs as configuration rather than hardcoding them, so
// tests can point a Provider at an httptest.Server instead of the real
// GitHub/Google APIs.
type Provider interface {
	Name() string
	AuthCodeURL(state, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier string) (accessToken string, err error)
	FetchIdentity(ctx context.Context, accessToken string) (Identity, error)
}

// ResolveOutcome tells the caller (the OAuth callback handler) what
// happened, so it can decide whether to show onboarding (task 1.6) or a
// "separate account created" notice.
type ResolveOutcome int

const (
	// OutcomeLinked: the identity matched an existing verified-email user
	// and was attached to it (or was already attached from a previous login).
	OutcomeLinked ResolveOutcome = iota
	// OutcomeNewUser: no safe link was possible (unverified email, no
	// email at all, or a verified match that this identity isn't itself
	// verified for) — a new, separate user was created.
	OutcomeNewUser
)

// ResolveUser implements task 1.1's identity-linking rule: link an
// incoming identity to an existing user IFF the provider says its email is
// verified AND that same email belongs to an existing user who has
// verified it too. Anything short of that creates a new, separate user —
// auto-linking on an unverified email is account takeover (register the
// victim's address on a lax provider, log in, get merged into their
// account).
//
// A user's email is only ever persisted when it arrived verified — never
// an unverified string — which is also what keeps this function from ever
// hitting the email UNIQUE constraint: two different provider identities
// with the same unverified-by-us email simply both end up with no email on
// file, rather than colliding.
func ResolveUser(ctx context.Context, db *sqlx.DB, provider string, identity Identity) (*global.User, ResolveOutcome, error) {
	if existing, err := global.IdentityByProviderSub(ctx, db, provider, identity.ProviderSub); err == nil {
		user, err := global.UserByID(ctx, db, existing.UserID)
		return user, OutcomeLinked, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}

	if identity.EmailVerified && identity.Email != "" {
		if existingUser, err := global.UserByVerifiedEmail(ctx, db, identity.Email); err == nil {
			if err := global.LinkIdentity(ctx, db, existingUser.ID, provider, identity.ProviderSub, &identity.Email); err != nil {
				return nil, 0, err
			}
			return existingUser, OutcomeLinked, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, 0, err
		}
	}

	newUser := global.NewUser{Name: identity.Name}
	if identity.EmailVerified && identity.Email != "" {
		newUser.Email = &identity.Email
		newUser.EmailVerified = true
	}
	user, err := global.CreateUser(ctx, db, newUser)
	if err != nil {
		return nil, 0, err
	}
	if err := global.LinkIdentity(ctx, db, user.ID, provider, identity.ProviderSub, emptyToNil(identity.Email)); err != nil {
		return nil, 0, err
	}
	return user, OutcomeNewUser, nil
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
