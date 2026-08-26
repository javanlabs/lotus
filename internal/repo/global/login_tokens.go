package global

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// ErrLoginTokenInvalid covers every redemption failure — unknown, expired,
// or already consumed. Collapsed into one sentinel deliberately: telling a
// caller which of the three it was tells an attacker whether a guessed
// token ever existed.
var ErrLoginTokenInvalid = errors.New("global: login token is invalid, expired, or already used")

type LoginToken struct {
	ID         string    `db:"id"`
	UserID     uuid.UUID `db:"user_id"`
	OrgID      uuid.UUID `db:"org_id"`
	ReturnPath string    `db:"return_path"`
}

type NewLoginToken struct {
	ID         string
	UserID     uuid.UUID
	OrgID      uuid.UUID
	ReturnPath string
	ExpiresAt  time.Time
}

func CreateLoginToken(ctx context.Context, db *sqlx.DB, t NewLoginToken) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO login_tokens (id, user_id, org_id, return_path, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.UserID, t.OrgID, t.ReturnPath, t.ExpiresAt)
	return err
}

// ConsumeLoginToken redeems a token exactly once. The UPDATE ... RETURNING
// is the whole point: checking then updating is a race whose name is
// "login replay", so validity and consumption happen in one statement and
// a second redemption simply matches no row.
func ConsumeLoginToken(ctx context.Context, db *sqlx.DB, idHash string) (*LoginToken, error) {
	var out LoginToken
	err := sqlx.GetContext(ctx, db, &out, `
		UPDATE login_tokens
		   SET consumed_at = now()
		 WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING id, user_id, org_id, return_path`, idHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLoginTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CountRecentLoginTokens backs the per-user rate limit. Without it, the
// request endpoint is a free mail-sending gadget pointed at one address.
func CountRecentLoginTokens(ctx context.Context, db *sqlx.DB, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := db.GetContext(ctx, &n,
		`SELECT count(*) FROM login_tokens WHERE user_id = $1 AND created_at > $2`, userID, since)
	return n, err
}

// DevUserSuffix is the email suffix a seeded development user must carry.
// .test is reserved by RFC 2606 and can never be a real address, so the
// dev-only "log in as" list below cannot name a genuine account even if
// the LOTUS_DEV gate that hides it were somehow bypassed.
const DevUserSuffix = "@lotus.test"

// DevUser is one entry in the dev login list.
type DevUser struct {
	ID    uuid.UUID `db:"id"`
	Email string    `db:"email"`
	Name  string    `db:"name"`
	Role  string    `db:"role"`
}

// DevUsers lists the seeded development users that hold a membership in
// org. Restricted to DevUserSuffix addresses — this is the third and
// narrowest of the gates around the dev login shortcut.
func DevUsers(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) ([]DevUser, error) {
	var out []DevUser
	err := db.SelectContext(ctx, &out, `
		SELECT u.id, u.email, u.name, r.key AS role
		  FROM users u
		  JOIN memberships m ON m.user_id = u.id AND m.org_id = $1 AND m.status = 'active'
		  JOIN roles r ON r.id = m.role_id
		 WHERE u.email LIKE '%' || $2
		 ORDER BY r.key`, orgID, DevUserSuffix)
	return out, err
}
