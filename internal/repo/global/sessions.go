package global

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Session mirrors the sessions table. ID is sha256(cookie value) hex, never
// the raw cookie — see task 1.3. OrgID nil means a global (auth-host)
// session; set means a tenant session.
type Session struct {
	ID         string     `db:"id"`
	UserID     uuid.UUID  `db:"user_id"`
	OrgID      *uuid.UUID `db:"org_id"`
	Host       string     `db:"host"`
	PermEpoch  int        `db:"perm_epoch"`
	IP         *string    `db:"ip"`
	UserAgent  *string    `db:"user_agent"`
	CreatedAt  time.Time  `db:"created_at"`
	LastSeenAt time.Time  `db:"last_seen_at"`
	ExpiresAt  time.Time  `db:"expires_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
}

type NewSession struct {
	ID        string // sha256(cookie value) hex
	UserID    uuid.UUID
	OrgID     *uuid.UUID
	Host      string
	PermEpoch int
	IP        *string
	UserAgent *string
	ExpiresAt time.Time
}

func CreateSession(ctx context.Context, db *sqlx.DB, s NewSession) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, org_id, host, perm_epoch, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.ID, s.UserID, s.OrgID, s.Host, s.PermEpoch, s.IP, s.UserAgent, s.ExpiresAt)
	return err
}

// SessionByID looks up a live session: not revoked, not past its absolute
// expiry, and seen more recently than idleThreshold (the caller computes
// this as time.Now().Add(-idleWindow) — tenant sessions use a 12h idle
// window, global sessions 14 days, task 1.3). A revoked, expired, or
// idled-out row simply doesn't come back — callers treat that the same as
// "no session" (sql.ErrNoRows), not as a distinct error, so a caller can't
// distinguish "never existed" from "revoked" by timing or error shape.
func SessionByID(ctx context.Context, db *sqlx.DB, id string, idleThreshold time.Time) (*Session, error) {
	var out Session
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, user_id, org_id, host, perm_epoch, ip, user_agent, created_at, last_seen_at, expires_at, revoked_at
		FROM sessions
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > now() AND last_seen_at > $2`, id, idleThreshold)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeSession marks a session revoked. Idempotent.
func RevokeSession(ctx context.Context, db *sqlx.DB, id string) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

// RevokeAllForUser revokes every live session for a user except keepID
// (pass "" to revoke all of them) — used by "log out all other devices".
func RevokeAllForUser(ctx context.Context, db *sqlx.DB, userID uuid.UUID, keepID string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL AND id <> $2`, userID, keepID)
	return err
}

// RotateSession replaces oldID with a new session row in one transaction:
// used on every role change / perm_epoch refresh (task 1.3) so a session
// that outlives a permission change also gets a fresh id, not just fresh
// permissions.
func RotateSession(ctx context.Context, db *sqlx.DB, oldID string, next NewSession) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, oldID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, org_id, host, perm_epoch, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		next.ID, next.UserID, next.OrgID, next.Host, next.PermEpoch, next.IP, next.UserAgent, next.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchLastSeen updates last_seen_at, but only if more than 60s have
// elapsed since the last update — avoids a write on every single request.
func TouchLastSeen(ctx context.Context, db *sqlx.DB, id string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = now()
		WHERE id = $1 AND last_seen_at < now() - interval '60 seconds'`, id)
	return err
}

// ListActiveSessions returns every live session for a user, most recently
// seen first — feeds the "active sessions" settings screen (task 1.3).
func ListActiveSessions(ctx context.Context, db *sqlx.DB, userID uuid.UUID) ([]Session, error) {
	var out []Session
	err := sqlx.SelectContext(ctx, db, &out, `
		SELECT id, user_id, org_id, host, perm_epoch, ip, user_agent, created_at, last_seen_at, expires_at, revoked_at
		FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_seen_at DESC`, userID)
	return out, err
}

// SweepExpiredSessions deletes rows past their expiry — keeps the table
// from growing unboundedly. Returns the number of rows removed.
func SweepExpiredSessions(ctx context.Context, db *sqlx.DB) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
