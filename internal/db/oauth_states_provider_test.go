package db_test

import (
	"testing"
	"time"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/dbtest"
)

// Every provider the registry can hand back must be a value the database
// will accept in oauth_states.provider.
//
// This exists because it did not. Task 4.7 added the "sso" provider and
// left the CHECK constraint listing only github and google, so the first
// person to click "Continue with SSO" on production got
//
//	new row for relation "oauth_states" violates check constraint
//	"oauth_states_provider_check" (SQLSTATE 23514)
//
// before the browser ever reached the identity provider. The whole login
// path had tests; none of them covered a provider name the column would
// reject, because the state row is written at the very first step and
// every existing test used a name that was already allowed.
//
// Driving this from authn.ProviderNames rather than a literal list is the
// point: adding a fourth provider without the matching migration fails
// here instead of failing a user.
func TestOAuthStatesAcceptsEveryRegisteredProviderName(t *testing.T) {
	conn := dbtest.Open(t)

	for _, name := range authn.ProviderNames {
		t.Run(name, func(t *testing.T) {
			_, err := conn.Exec(
				`INSERT INTO oauth_states (state_hash, provider, code_verifier, expires_at)
				 VALUES ($1, $2, 'verifier', $3)`,
				"hash-"+name, name, time.Now().Add(10*time.Minute),
			)
			if err != nil {
				t.Fatalf("provider %q is registered in Go but rejected by the database: %v", name, err)
			}
		})
	}
}

// The column stays enumerated, so a typo cannot quietly become a state row
// nobody can ever redeem.
func TestOAuthStatesRejectsAnUnknownProviderName(t *testing.T) {
	conn := dbtest.Open(t)

	_, err := conn.Exec(
		`INSERT INTO oauth_states (state_hash, provider, code_verifier, expires_at)
		 VALUES ('hash-typo', 'githubb', 'verifier', $1)`,
		time.Now().Add(10*time.Minute),
	)
	if err == nil {
		t.Fatal("a misspelled provider was accepted; the CHECK constraint is not doing its job")
	}
}
