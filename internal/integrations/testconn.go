// Package integrations holds the minimal, provider-specific "prove this
// credential actually authenticates" calls task 2.2's Test Connection
// needs. Each call here is deliberately the cheapest request that answers
// "am I who I say I am, and roughly how much can I see" — full data
// collection (branches, findings, images, issues) is Fase 3's job
// (tasks 3.3–3.5); this package exists so task 2.2 doesn't have to wait
// for it to ship something real rather than a fake "always ok" stub.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Result is what a test-connection check reports back. OK distinguishes
// "the request completed and the credential was rejected" (still a
// perfectly good, informative result) from a transport-level error
// (network unreachable, SSRF-blocked, timeout) — see the AuthFailed field.
type Result struct {
	OK         bool
	AuthFailed bool // true only for a clean 401/403 from the upstream
	Summary    string
}

// Test dispatches to the provider-specific check. client is expected to be
// an SSRF-guarded client (internal/netguard) — this package has no opinion
// on that itself, it just uses whatever *http.Client it's given.
func Test(ctx context.Context, client *http.Client, provider, baseURL, secret string) (Result, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	switch provider {
	case "gitlab":
		return testGitLab(ctx, client, baseURL, secret)
	case "sonarqube":
		return testSonarQube(ctx, client, baseURL, secret)
	case "harbor_trivy":
		return testHarbor(ctx, client, baseURL, secret)
	case "sentry":
		return testSentry(ctx, client, baseURL, secret)
	default:
		return Result{}, fmt.Errorf("integrations: unknown provider %q", provider)
	}
}

func get(ctx context.Context, client *http.Client, url string, setHeaders func(*http.Request)) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if setHeaders != nil {
		setHeaders(req)
	}
	return client.Do(req)
}

// testGitLab calls GET /api/v4/user (PRIVATE-TOKEN header) — GitLab's
// cheapest "who am I" endpoint — then GET /api/v4/projects?membership=true
// &per_page=1 and reads the X-Total response header for a project count,
// without paginating through the actual list.
func testGitLab(ctx context.Context, client *http.Client, baseURL, secret string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/v4/user", func(r *http.Request) {
		r.Header.Set("PRIVATE-TOKEN", secret)
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by GitLab"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("GitLab returned HTTP %d", resp.StatusCode)}, nil
	}
	var user struct {
		Username string `json:"username"`
	}
	json.NewDecoder(resp.Body).Decode(&user) //nolint:errcheck // best-effort summary only

	total := "unknown"
	if projResp, err := get(ctx, client, baseURL+"/api/v4/projects?membership=true&per_page=1", func(r *http.Request) {
		r.Header.Set("PRIVATE-TOKEN", secret)
	}); err == nil {
		defer projResp.Body.Close()
		if v := projResp.Header.Get("X-Total"); v != "" {
			total = v
		}
	}
	return Result{OK: true, Summary: fmt.Sprintf("authenticated as %s, %s project(s) visible", user.Username, total)}, nil
}

// testSonarQube calls GET /api/authentication/validate with the token as
// the Basic auth username (SonarQube's own convention — the password is
// left empty).
func testSonarQube(ctx context.Context, client *http.Client, baseURL, secret string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/authentication/validate", func(r *http.Request) {
		r.SetBasicAuth(secret, "")
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, AuthFailed: resp.StatusCode == http.StatusUnauthorized, Summary: fmt.Sprintf("SonarQube returned HTTP %d", resp.StatusCode)}, nil
	}
	var body struct {
		Valid bool `json:"valid"`
	}
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck

	total := "unknown"
	if searchResp, err := get(ctx, client, baseURL+"/api/projects/search?ps=1", func(r *http.Request) {
		r.SetBasicAuth(secret, "")
	}); err == nil {
		defer searchResp.Body.Close()
		var page struct {
			Paging struct {
				Total int `json:"total"`
			} `json:"paging"`
		}
		if json.NewDecoder(searchResp.Body).Decode(&page) == nil {
			total = strconv.Itoa(page.Paging.Total)
		}
	}
	if !body.Valid {
		return Result{OK: false, AuthFailed: true, Summary: "SonarQube rejected the token"}, nil
	}
	return Result{OK: true, Summary: fmt.Sprintf("token valid, %s project(s) visible", total)}, nil
}

// testHarbor calls GET /api/v2.0/users/current with Basic auth (robot
// account credentials are a "name:secret"-shaped username in Harbor,
// passed through unchanged as this function's secret parameter — task
// 2.3's per-binding config, not this function, owns any further
// interpretation of that shape).
func testHarbor(ctx context.Context, client *http.Client, baseURL, secret string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/v2.0/users/current", func(r *http.Request) {
		user, pass, ok := strings.Cut(secret, ":")
		if !ok {
			user, pass = secret, ""
		}
		r.SetBasicAuth(user, pass)
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by Harbor"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("Harbor returned HTTP %d", resp.StatusCode)}, nil
	}
	var user struct {
		Username string `json:"username"`
	}
	json.NewDecoder(resp.Body).Decode(&user) //nolint:errcheck
	return Result{OK: true, Summary: fmt.Sprintf("authenticated as %s", user.Username)}, nil
}

// testSentry calls GET /api/0/organizations/ with a Bearer token — the
// cheapest call that both proves the token works and reports how many
// organizations it can see.
func testSentry(ctx context.Context, client *http.Client, baseURL, secret string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/0/organizations/", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by Sentry"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("Sentry returned HTTP %d", resp.StatusCode)}, nil
	}
	var orgs []struct {
		Slug string `json:"slug"`
	}
	json.NewDecoder(resp.Body).Decode(&orgs) //nolint:errcheck
	return Result{OK: true, Summary: fmt.Sprintf("authenticated, %d organization(s) visible", len(orgs))}, nil
}
