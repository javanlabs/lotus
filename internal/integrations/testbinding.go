package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// TestBinding is task 2.3's "panggil upstream sekali dan laporin apa yang
// ketemu" — a single existence check against the specific external_key a
// binding claims, so a typo'd project path is caught immediately rather
// than surfacing later as "zero results" from a scheduled sync. Minimal by
// the same design choice as Test (task 2.2): full data collection is
// Fase 3's job.
func TestBinding(ctx context.Context, client *http.Client, kind, baseURL, secret, externalKey string) (Result, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	switch kind {
	case "gitlab":
		return testBindingGitLab(ctx, client, baseURL, secret, externalKey)
	case "sonarqube":
		return testBindingSonarQube(ctx, client, baseURL, secret, externalKey)
	case "trivy":
		return testBindingHarbor(ctx, client, baseURL, secret, externalKey)
	case "sentry":
		return testBindingSentry(ctx, client, baseURL, secret, externalKey)
	default:
		return Result{}, fmt.Errorf("integrations: unknown binding kind %q", kind)
	}
}

func testBindingGitLab(ctx context.Context, client *http.Client, baseURL, secret, externalKey string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/v4/projects/"+url.PathEscape(externalKey), func(r *http.Request) {
		r.Header.Set("PRIVATE-TOKEN", secret)
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Result{OK: false, Summary: fmt.Sprintf("no GitLab project found at %q", externalKey)}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by GitLab"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("GitLab returned HTTP %d", resp.StatusCode)}, nil
	}
	var proj struct {
		NameWithNamespace string `json:"name_with_namespace"`
	}
	json.NewDecoder(resp.Body).Decode(&proj) //nolint:errcheck
	return Result{OK: true, Summary: fmt.Sprintf("found %q", proj.NameWithNamespace)}, nil
}

func testBindingSonarQube(ctx context.Context, client *http.Client, baseURL, secret, externalKey string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/components/show?component="+url.QueryEscape(externalKey), func(r *http.Request) {
		r.SetBasicAuth(secret, "")
	})
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Result{OK: false, Summary: fmt.Sprintf("no SonarQube project found with key %q", externalKey)}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by SonarQube"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("SonarQube returned HTTP %d", resp.StatusCode)}, nil
	}
	var body struct {
		Component struct {
			Name string `json:"name"`
		} `json:"component"`
	}
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	return Result{OK: true, Summary: fmt.Sprintf("found %q", body.Component.Name)}, nil
}

// testBindingHarbor expects externalKey shaped "project/repo" (task 2.3's
// own table: "Harbor repo, alurkerja-v2/alurkerja-bpm-release"). Harbor's
// API requires the repository segment double-escaped when it itself
// contains a slash (its own well-known quirk), handled by escaping the
// repo portion once and leaving the project portion as a normal path
// segment.
func testBindingHarbor(ctx context.Context, client *http.Client, baseURL, secret, externalKey string) (Result, error) {
	project, repo, ok := strings.Cut(externalKey, "/")
	if !ok {
		return Result{OK: false, Summary: `external_key must be shaped "project/repo"`}, nil
	}
	resp, err := get(ctx, client, baseURL+"/api/v2.0/projects/"+url.PathEscape(project)+"/repositories/"+url.PathEscape(repo), func(r *http.Request) {
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
	if resp.StatusCode == http.StatusNotFound {
		return Result{OK: false, Summary: fmt.Sprintf("no Harbor repository found at %q", externalKey)}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Result{OK: false, AuthFailed: true, Summary: "authentication rejected by Harbor"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Summary: fmt.Sprintf("Harbor returned HTTP %d", resp.StatusCode)}, nil
	}
	return Result{OK: true, Summary: fmt.Sprintf("found repository %q", externalKey)}, nil
}

// testBindingSentry lists projects and checks externalKey (a project
// slug) is among them — Sentry's API is organization-scoped in a way this
// binding's config doesn't carry an org slug for, so "does this project
// slug appear anywhere this token can see" is the honest minimal check
// available here; Fase 3's real collector resolves the org properly.
func testBindingSentry(ctx context.Context, client *http.Client, baseURL, secret, externalKey string) (Result, error) {
	resp, err := get(ctx, client, baseURL+"/api/0/projects/", func(r *http.Request) {
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
	var projects []struct {
		Slug string `json:"slug"`
	}
	json.NewDecoder(resp.Body).Decode(&projects) //nolint:errcheck
	for _, p := range projects {
		if p.Slug == externalKey {
			return Result{OK: true, Summary: fmt.Sprintf("found project %q", externalKey)}, nil
		}
	}
	return Result{OK: false, Summary: fmt.Sprintf("no visible Sentry project with slug %q", externalKey)}, nil
}
