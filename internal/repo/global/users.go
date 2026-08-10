package global

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// User is the control-plane identity row. Email is nullable: a provider
// (GitHub, in practice) can hand back neither a public nor a verified
// email, and task 1.1 requires creating the user anyway rather than
// rejecting the login.
type User struct {
	ID              uuid.UUID  `db:"id"`
	Email           *string    `db:"email"`
	Name            string     `db:"name"`
	AvatarURL       *string    `db:"avatar_url"`
	EmailVerifiedAt *time.Time `db:"email_verified_at"`
	Status          string     `db:"status"`
}

// Identity links one user to one (provider, provider_sub) pair.
type Identity struct {
	ID          uuid.UUID `db:"id"`
	UserID      uuid.UUID `db:"user_id"`
	Provider    string    `db:"provider"`
	ProviderSub string    `db:"provider_sub"`
	EmailAtLink *string   `db:"email_at_link"`
}

// NewUser is the input to CreateUser. Email/AvatarURL are nil when the
// provider didn't supply them; EmailVerified controls whether
// email_verified_at is stamped, which is what UserByVerifiedEmail keys on
// for the identity-linking decision (task 1.1).
type NewUser struct {
	Email         *string
	EmailVerified bool
	Name          string
	AvatarURL     *string
}

func CreateUser(ctx context.Context, db *sqlx.DB, u NewUser) (*User, error) {
	var verifiedAt *time.Time
	if u.EmailVerified && u.Email != nil {
		now := time.Now().UTC()
		verifiedAt = &now
	}
	var out User
	err := sqlx.GetContext(ctx, db, &out, `
		INSERT INTO users (email, name, avatar_url, email_verified_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, email, name, avatar_url, email_verified_at, status`,
		lowerOrNil(u.Email), u.Name, u.AvatarURL, verifiedAt)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UserByVerifiedEmail returns the user whose email matches (case-
// insensitively — email is already stored lowercased) AND who has
// verified that email. This is deliberately narrower than "any user with
// this email": task 1.1's linking rule requires the existing account's
// email to be verified too, not just the incoming one, or account takeover
// via an unverified duplicate becomes possible.
func UserByVerifiedEmail(ctx context.Context, db *sqlx.DB, email string) (*User, error) {
	var out User
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, email, name, avatar_url, email_verified_at, status
		FROM users WHERE email = $1 AND email_verified_at IS NOT NULL`, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func UserByID(ctx context.Context, db *sqlx.DB, id uuid.UUID) (*User, error) {
	var out User
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, email, name, avatar_url, email_verified_at, status
		FROM users WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func IdentityByProviderSub(ctx context.Context, db *sqlx.DB, provider, sub string) (*Identity, error) {
	var out Identity
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, user_id, provider, provider_sub, email_at_link
		FROM user_identities WHERE provider = $1 AND provider_sub = $2`, provider, sub)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkIdentity attaches a (provider, provider_sub) pair to userID.
// Constrained by the DB's UNIQUE(provider, provider_sub) — a duplicate
// attach attempt returns that constraint's error, not a silent no-op.
func LinkIdentity(ctx context.Context, db *sqlx.DB, userID uuid.UUID, provider, sub string, emailAtLink *string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO user_identities (user_id, provider, provider_sub, email_at_link)
		VALUES ($1, $2, $3, $4)`, userID, provider, sub, lowerOrNil(emailAtLink))
	return err
}

// TouchLastLogin stamps last_login_at = now(). Best-effort: a failure here
// shouldn't fail the login itself.
func TouchLastLogin(ctx context.Context, db *sqlx.DB, userID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return err
}

// UpdateUserName sets a user's display name — task 1.6's /welcome screen.
func UpdateUserName(ctx context.Context, db *sqlx.DB, userID uuid.UUID, name string) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET name = $1 WHERE id = $2`, name, userID)
	return err
}

func lowerOrNil(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*s))
	return &v
}
