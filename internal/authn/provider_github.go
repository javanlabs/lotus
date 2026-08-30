package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitHubProvider talks to GitHub's OAuth + REST API. Base URLs default to
// the real GitHub but are overridable so tests can point at an
// httptest.Server instead.
type GitHubProvider struct {
	ClientID, ClientSecret, RedirectURL string
	AuthBaseURL, TokenURL, APIBaseURL   string
	HTTPClient                          *http.Client
}

func NewGitHubProvider(clientID, clientSecret, redirectURL string) *GitHubProvider {
	return &GitHubProvider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		AuthBaseURL:  "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		APIBaseURL:   "https://api.github.com",
		HTTPClient:   http.DefaultClient,
	}
}

func (p *GitHubProvider) Name() string { return "github" }

// AuthCodeURL requests only read:user user:email — never repo. Lotus calls
// GitLab/Sonar/Sentry with organization-owned credentials (task 2.2), never
// on a user's behalf, so a user-scoped GitHub token has no consumer here.
func (p *GitHubProvider) AuthCodeURL(state, codeChallenge string) (string, error) {
	v := url.Values{
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURL},
		"scope":                 {"read:user user:email"},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	// Endpoints are static for this provider, so this never fails.
	return p.AuthBaseURL + "?" + v.Encode(), nil
}

func (p *GitHubProvider) Exchange(ctx context.Context, code, codeVerifier string) (string, error) {
	form := url.Values{
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"code_verifier": {codeVerifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github token exchange: status %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Error != "" {
		return "", fmt.Errorf("github token exchange: %s", body.Error)
	}
	return body.AccessToken, nil
}

// FetchIdentity implements the per-provider trap from task 1.1: GET /user
// can return email:null or an @users.noreply.github.com address, neither
// of which may ever be matched against an existing account. The only
// source of truth for a usable email is GET /user/emails, filtered to
// primary && verified.
func (p *GitHubProvider) FetchIdentity(ctx context.Context, accessToken string) (Identity, error) {
	var user struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := p.getJSON(ctx, accessToken, "/user", &user); err != nil {
		return Identity{}, err
	}

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := p.getJSON(ctx, accessToken, "/user/emails", &emails); err != nil {
		return Identity{}, err
	}

	name := user.Name
	if name == "" {
		name = user.Login // GitHub's "name" (display name) is often unset; login never is
	}
	identity := Identity{ProviderSub: strconv.FormatInt(user.ID, 10), Name: name}
	for _, e := range emails {
		if e.Primary && e.Verified {
			identity.Email = e.Email
			identity.EmailVerified = true
			break
		}
	}
	return identity, nil
}

func (p *GitHubProvider) getJSON(ctx context.Context, accessToken, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.APIBaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github %s: status %d: %s", path, resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
