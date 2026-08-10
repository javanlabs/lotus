package global

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type NewOAuthState struct {
	StateHash    string
	Provider     string
	CodeVerifier string
	ReturnPath   string
	ExpiresAt    time.Time
}

func CreateOAuthState(ctx context.Context, db *sqlx.DB, s NewOAuthState) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO oauth_states (state_hash, provider, code_verifier, return_path, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		s.StateHash, s.Provider, s.CodeVerifier, s.ReturnPath, s.ExpiresAt)
	return err
}

// ConsumedOAuthState is what ConsumeOAuthState returns: enough to complete
// the exchange, and to double-check the provider named in the callback URL
// matches the provider the flow was actually started for.
type ConsumedOAuthState struct {
	Provider     string `db:"provider"`
	CodeVerifier string `db:"code_verifier"`
	ReturnPath   string `db:"return_path"`
}

// ConsumeOAuthState atomically marks a state row consumed and returns it,
// or sql.ErrNoRows if it doesn't exist, already expired, or was already
// consumed — read-then-write here would be a replay window, same class of
// bug as the handoff code in task 1.2.
func ConsumeOAuthState(ctx context.Context, db *sqlx.DB, stateHash string) (*ConsumedOAuthState, error) {
	var out ConsumedOAuthState
	err := sqlx.GetContext(ctx, db, &out, `
		UPDATE oauth_states SET consumed_at = now()
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING provider, code_verifier, return_path`, stateHash)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
