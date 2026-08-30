package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GoogleProvider talks to Google's OAuth + OpenID Connect userinfo
// endpoint. Using the REST userinfo endpoint (rather than parsing/
// verifying the ID token JWT against Google's JWKS) keeps this symmetric
// with GitHubProvider and just as easy to point at a fake server in tests;
// it's also what Google's own docs list as a supported way to fetch
// profile claims from an access token.
type GoogleProvider struct {
	ClientID, ClientSecret, RedirectURL string
	AuthBaseURL, TokenURL, UserInfoURL  string
	HTTPClient                          *http.Client
}

func NewGoogleProvider(clientID, clientSecret, redirectURL string) *GoogleProvider {
	return &GoogleProvider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		AuthBaseURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		UserInfoURL:  "https://openidconnect.googleapis.com/v1/userinfo",
		HTTPClient:   http.DefaultClient,
	}
}

func (p *GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) AuthCodeURL(state, codeChallenge string) (string, error) {
	v := url.Values{
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	// Endpoints are static for this provider, so this never fails.
	return p.AuthBaseURL + "?" + v.Encode(), nil
}

func (p *GoogleProvider) Exchange(ctx context.Context, code, codeVerifier string) (string, error) {
	form := url.Values{
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"code_verifier": {codeVerifier},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
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
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("google token exchange: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.AccessToken, nil
}

// FetchIdentity trusts the email_verified claim as-is, including for
// consumer Gmail addresses — that's the expected, correct behavior per
// task 1.1, not a gap.
func (p *GoogleProvider) FetchIdentity(ctx context.Context, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
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
		b, _ := io.ReadAll(resp.Body)
		return Identity{}, fmt.Errorf("google userinfo: status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Identity{}, err
	}
	return Identity{ProviderSub: body.Sub, Email: body.Email, EmailVerified: body.EmailVerified, Name: body.Name}, nil
}
