package global

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// APIToken is a Personal Access Token (UserID set, OrgID nil) or an
// Organization Service Token (OrgID set, UserID nil) — task 1.8.
//
// Scopes/IPAllowlist are pq.StringArray, not plain []string: that's what
// makes sqlx's automatic struct scanning understand Postgres's text[]
// wire format on the way in and out. pq.StringArray is defined as
// []string underneath, so callers can still range over it, pass it to
// slices.Contains, etc. without a conversion.
type APIToken struct {
	ID          uuid.UUID      `db:"id"`
	Kind        string         `db:"kind"` // "pat" | "service"
	UserID      *uuid.UUID     `db:"user_id"`
	OrgID       *uuid.UUID     `db:"org_id"`
	Name        string         `db:"name"`
	TokenPrefix string         `db:"token_prefix"`
	Scopes      pq.StringArray `db:"scopes"`
	ProjectID   *uuid.UUID     `db:"project_id"`
	ComponentID *uuid.UUID     `db:"component_id"`
	IPAllowlist pq.StringArray `db:"ip_allowlist"`
	ExpiresAt   *time.Time     `db:"expires_at"`
	CreatedBy   *uuid.UUID     `db:"created_by"` // nil once the creating user has been deleted
	LastUsedAt  *time.Time     `db:"last_used_at"`
	LastUsedIP  *string        `db:"last_used_ip"`
	RevokedAt   *time.Time     `db:"revoked_at"`
}

type NewAPIToken struct {
	Kind        string
	UserID      *uuid.UUID
	OrgID       *uuid.UUID
	Name        string
	TokenHash   string
	TokenPrefix string
	Scopes      []string
	ProjectID   *uuid.UUID
	ComponentID *uuid.UUID
	IPAllowlist []string
	ExpiresAt   *time.Time
	CreatedBy   uuid.UUID
}

func CreateAPIToken(ctx context.Context, db *sqlx.DB, t NewAPIToken) (uuid.UUID, error) {
	var id uuid.UUID
	err := sqlx.GetContext(ctx, db, &id, `
		INSERT INTO api_tokens (kind, user_id, org_id, name, token_hash, token_prefix, scopes,
		                         project_id, component_id, ip_allowlist, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id`,
		t.Kind, t.UserID, t.OrgID, t.Name, t.TokenHash, t.TokenPrefix, pq.Array(t.Scopes),
		t.ProjectID, t.ComponentID, pq.Array(t.IPAllowlist), t.ExpiresAt, t.CreatedBy)
	return id, err
}

// APITokenByHash returns a live token: unrevoked and (if it has an
// expiry) unexpired. Checksum verification (task 1.8: "checksum yang
// salah ditolak tanpa nyentuh database") happens before this is ever
// called — see ParseToken — so a mistyped token never reaches this query
// at all.
func APITokenByHash(ctx context.Context, db *sqlx.DB, tokenHash string) (*APIToken, error) {
	var out APIToken
	err := sqlx.GetContext(ctx, db, &out, `
		SELECT id, kind, user_id, org_id, name, token_prefix, scopes, project_id, component_id,
		       ip_allowlist, expires_at, created_by, last_used_at, last_used_ip, revoked_at
		FROM api_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, tokenHash)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// TouchAPITokenUsage records last_used_at/last_used_ip. Best-effort: a
// failure here shouldn't fail the request the token was authenticating.
func TouchAPITokenUsage(ctx context.Context, db *sqlx.DB, id uuid.UUID, ip string) error {
	_, err := db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = now(), last_used_ip = $2 WHERE id = $1`, id, ip)
	return err
}

func RevokeAPIToken(ctx context.Context, db *sqlx.DB, id uuid.UUID) error {
	_, err := db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

// ListAPITokensForUser lists a user's own live PATs — Settings -> Tokens
// (task 4.5 builds the page; this is its engine).
func ListAPITokensForUser(ctx context.Context, db *sqlx.DB, userID uuid.UUID) ([]APIToken, error) {
	var out []APIToken
	err := sqlx.SelectContext(ctx, db, &out, `
		SELECT id, kind, user_id, org_id, name, token_prefix, scopes, project_id, component_id,
		       ip_allowlist, expires_at, created_by, last_used_at, last_used_ip, revoked_at
		FROM api_tokens
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC`, userID)
	return out, err
}

// ListAPITokensForOrg lists an org's live service tokens.
func ListAPITokensForOrg(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) ([]APIToken, error) {
	var out []APIToken
	err := sqlx.SelectContext(ctx, db, &out, `
		SELECT id, kind, user_id, org_id, name, token_prefix, scopes, project_id, component_id,
		       ip_allowlist, expires_at, created_by, last_used_at, last_used_ip, revoked_at
		FROM api_tokens
		WHERE org_id = $1 AND kind = 'service' AND revoked_at IS NULL
		ORDER BY created_at DESC`, orgID)
	return out, err
}
