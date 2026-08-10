package authn

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo/global"
)

// ErrServiceTokenRequiresAdmin: service tokens are an org-wide credential
// (CI's own access to the org), not a personal convenience — creating one
// needs the same bar as touching integration credentials (task 1.4:
// connection.secret.write is Admin+ only), so this matches that.
var ErrServiceTokenRequiresAdmin = errors.New("only an Admin or Owner can create an organization service token")

// NewPersonalAccessTokenParams is the input to IssuePersonalAccessToken.
type NewPersonalAccessTokenParams struct {
	UserID    uuid.UUID
	Name      string
	Scopes    []string
	ExpiresAt *time.Time
}

// IssuePersonalAccessToken creates a PAT. Self-service: any signed-in user
// may create their own — there is no "for someone else" case, so no
// escalation check applies here the way it does for service tokens.
func IssuePersonalAccessToken(ctx context.Context, deps Deps, p NewPersonalAccessTokenParams) (raw string, err error) {
	raw, hash, err := GenerateAPIToken("pat")
	if err != nil {
		return "", err
	}
	_, err = global.CreateAPIToken(ctx, deps.DB, global.NewAPIToken{
		Kind: "pat", UserID: &p.UserID, Name: p.Name,
		TokenHash: hash, TokenPrefix: TokenPrefixForDisplay(raw),
		Scopes: p.Scopes, ExpiresAt: p.ExpiresAt, CreatedBy: p.UserID,
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// NewServiceTokenParams is the input to IssueServiceToken.
type NewServiceTokenParams struct {
	OrgID       uuid.UUID
	ActorID     uuid.UUID
	ActorRole   string
	Name        string
	Scopes      []string
	ProjectID   *uuid.UUID
	ComponentID *uuid.UUID
	IPAllowlist []string
	ExpiresAt   *time.Time
}

// IssueServiceToken creates an organization service token. Requires
// ActorRole to be admin or owner.
func IssueServiceToken(ctx context.Context, deps Deps, p NewServiceTokenParams) (raw string, err error) {
	if p.ActorRole != "admin" && p.ActorRole != "owner" {
		return "", ErrServiceTokenRequiresAdmin
	}
	raw, hash, err := GenerateAPIToken("service")
	if err != nil {
		return "", err
	}
	_, err = global.CreateAPIToken(ctx, deps.DB, global.NewAPIToken{
		Kind: "service", OrgID: &p.OrgID, Name: p.Name,
		TokenHash: hash, TokenPrefix: TokenPrefixForDisplay(raw),
		Scopes: p.Scopes, ProjectID: p.ProjectID, ComponentID: p.ComponentID,
		IPAllowlist: p.IPAllowlist, ExpiresAt: p.ExpiresAt, CreatedBy: p.ActorID,
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}
