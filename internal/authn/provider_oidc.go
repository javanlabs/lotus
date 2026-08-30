package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OIDCProvider talks to any standards-compliant OpenID Connect issuer —
// Keycloak, Authentik, Okta, Entra — discovered from its issuer URL rather
// than configured endpoint by endpoint. This is task 4.7's "generic OIDC
// provider, configured by environment variable": Lotus should not need a
// new Go type per customer IdP the way it has one for GitHub and one for
// Google.
//
// Identity comes from the userinfo endpoint rather than by verifying the
// ID token against the issuer's JWKS. That keeps this symmetric with the
// other two providers and just as easy to point at an httptest server,
// and it is safe here because the token never leaves our own
// back-channel: it is fetched over TLS directly from the token endpoint,
// so there is no untrusted intermediary whose signature we would need the
// JWKS to catch.
type OIDCProvider struct {
	// Issuer is the base URL whose /.well-known/openid-configuration
	// describes the rest, e.g. https://sso.javan.id/realms/internal.
	Issuer                              string
	ClientID, ClientSecret, RedirectURL string
	HTTPClient                          *http.Client

	// Discovery is cached after the first success and retried after every
	// failure. Doing it lazily rather than at construction matters: the
	// IdP is the only way into a single-mode instance, so making startup
	// depend on it would turn a brief IdP outage into an app that cannot
	// boot — including for everyone holding a valid session.
	mu        sync.Mutex
	endpoints *oidcEndpoints
}

type oidcEndpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	UserInfo      string `json:"userinfo_endpoint"`
}

// NewOIDCProvider does no network I/O; see the Discovery note above.
func NewOIDCProvider(issuer, clientID, clientSecret, redirectURL string) *OIDCProvider {
	return &OIDCProvider{
		Issuer:       strings.TrimSuffix(issuer, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Name is the registry key, the URL segment in /login/<name>, and what the
// login page labels the button with. "sso" rather than the issuer's own
// product name: which OIDC server sits behind it is the operator's
// business, not something to leak onto a login page.
func (p *OIDCProvider) Name() string { return "sso" }

func (p *OIDCProvider) discover(ctx context.Context) (*oidcEndpoints, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endpoints != nil {
		return p.endpoints, nil
	}

	docURL := p.Issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery %s: %w", docURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("oidc discovery %s: status %d: %s", docURL, resp.StatusCode, b)
	}
	var doc oidcEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("oidc discovery %s: %w", docURL, err)
	}
	// A document missing any of the three is not usable, and finding that
	// out here beats redirecting the browser to the empty string.
	if doc.Authorization == "" || doc.Token == "" || doc.UserInfo == "" {
		return nil, fmt.Errorf("oidc discovery %s: incomplete document "+
			"(authorization=%q token=%q userinfo=%q)", docURL, doc.Authorization, doc.Token, doc.UserInfo)
	}
	p.endpoints = &doc
	return p.endpoints, nil
}

func (p *OIDCProvider) AuthCodeURL(state, codeChallenge string) (string, error) {
	// The Provider interface hands no context to this method, and adding
	// one would reach into both other providers for no benefit — neither
	// makes a request here. The timeout is what actually matters: an
	// unreachable IdP must fail the login promptly, not hang the request.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoints, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	v := url.Values{
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return endpoints.Authorization + "?" + v.Encode(), nil
}

func (p *OIDCProvider) Exchange(ctx context.Context, code, codeVerifier string) (string, error) {
	endpoints, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"code_verifier": {codeVerifier},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("oidc token exchange: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.AccessToken, nil
}

// FetchIdentity reads the standard OIDC claims. email_verified is taken at
// face value, exactly as the Google provider does — and it carries real
// weight here, because it is what ClaimOwnershipIfPending requires before
// handing a fresh single-mode instance to its first Owner. An IdP
// configured to leave the claim false (or omit it) will refuse that claim
// rather than trust a self-asserted address.
func (p *OIDCProvider) FetchIdentity(ctx context.Context, accessToken string) (Identity, error) {
	endpoints, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoints.UserInfo, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Identity{}, fmt.Errorf("oidc userinfo: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Identity{}, err
	}
	// Keycloak populates `name` only when both given and family name are
	// set on the account; preferred_username is always there. This only
	// prefills the /welcome form, so a username beats an empty field.
	name := body.Name
	if name == "" {
		name = body.PreferredUsername
	}
	return Identity{
		ProviderSub:   body.Sub,
		Email:         body.Email,
		EmailVerified: body.EmailVerified,
		Name:          name,
	}, nil
}
