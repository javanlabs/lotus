package global

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Handshake is the row created on the tenant host before any auth happens
// (task 1.2 step 1).
type Handshake struct {
	ID           uuid.UUID `db:"id"`
	OrgID        uuid.UUID `db:"org_id"`
	ReturnPath   string    `db:"return_path"`
	VerifierHash string    `db:"verifier_hash"`
	ExpiresAt    time.Time `db:"expires_at"`
}

type NewHandshake struct {
	OrgID        uuid.UUID
	ReturnPath   string
	VerifierHash string
	ExpiresAt    time.Time
}

func CreateHandshake(ctx context.Context, db *sqlx.DB, h NewHandshake) (uuid.UUID, error) {
	var id uuid.UUID
	err := sqlx.GetContext(ctx, db, &id, `
		INSERT INTO handoff_handshakes (org_id, return_path, verifier_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, h.OrgID, h.ReturnPath, h.VerifierHash, h.ExpiresAt)
	return id, err
}

// HandshakeByID returns a still-live (unexpired) handshake, or
// sql.ErrNoRows. Handshakes aren't marked consumed — see auth_tickets for
// the actual single-use security boundary; a handshake existing is just
// context carried across one redirect.
func HandshakeByID(ctx context.Context, db *sqlx.DB, id uuid.UUID) (*Handshake, error) {
	var out Handshake
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, org_id, return_path, verifier_hash, expires_at
		FROM handoff_handshakes WHERE id = $1 AND expires_at > now()`, id)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// NewAuthTicket is the single-use code minted once the auth host has
// authorized the user for a specific organization (task 1.2 step 3).
type NewAuthTicket struct {
	TokenHash    string
	UserID       uuid.UUID
	OrgID        uuid.UUID
	VerifierHash string
	ReturnPath   string
	ExpiresAt    time.Time
}

func CreateAuthTicket(ctx context.Context, db *sqlx.DB, t NewAuthTicket) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO auth_tickets (token_hash, user_id, org_id, verifier_hash, return_path, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		t.TokenHash, t.UserID, t.OrgID, t.VerifierHash, t.ReturnPath, t.ExpiresAt)
	return err
}

// ConsumedAuthTicket is what ConsumeAuthTicket returns.
type ConsumedAuthTicket struct {
	UserID       uuid.UUID `db:"user_id"`
	OrgID        uuid.UUID `db:"org_id"`
	VerifierHash string    `db:"verifier_hash"`
	ReturnPath   string    `db:"return_path"`
}

// ConsumeAuthTicket atomically marks the ticket consumed and returns it, or
// sql.ErrNoRows if it doesn't exist, is expired, or was already consumed.
// The UPDATE...WHERE consumed_at IS NULL...RETURNING shape is what makes
// this atomic: a read-then-write here would be a login-replay race (two
// concurrent requests both seeing "not yet consumed" and both succeeding).
func ConsumeAuthTicket(ctx context.Context, db *sqlx.DB, tokenHash string) (*ConsumedAuthTicket, error) {
	var out ConsumedAuthTicket
	err := sqlx.GetContext(ctx, db, &out, `
		UPDATE auth_tickets SET consumed_at = now()
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING user_id, org_id, verifier_hash, return_path`, tokenHash)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
