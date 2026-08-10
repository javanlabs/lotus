package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DiscoveredItem is one resource a provider reports as visible to a
// connection's credential — task 2.4's raison d'être: the alternative is
// typing a project path by hand and getting zero results on a typo with
// no error at all (the old Lotus's services.yaml comment block, quoted
// almost verbatim in the spec, is an apology for exactly this).
type DiscoveredItem struct {
	ExternalKey string
	Label       string
	Metadata    map[string]any
}

// DiscoverDegradedNoAdminScope is a non-error outcome: the credential is
// valid but lacks the scope needed to list everything (task 2.4's
// SonarQube case). The caller should fall back to a free-text external_key
// input + Test Binding, not show an error.
var DiscoverDegradedNoAdminScope = fmt.Errorf("integrations: credential lacks admin scope for discovery")

// Discover lists what a connection's credential can see, keyed by
// Connection.Provider (not a binding Kind — "harbor_trivy" here, not
// "trivy", matching the connections.provider CHECK constraint). Bounded
// (page caps below) rather than exhaustive: task 2.4 asks for "muncul
// dalam hitungan detik", and an organization with tens of thousands of
// upstream projects is better served by search-as-you-type over the
// discovered set than by one request trying to fetch all of them.
func Discover(ctx context.Context, client *http.Client, provider, baseURL, secret string) ([]DiscoveredItem, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	switch provider {
	case "gitlab":
		return discoverGitLab(ctx, client, baseURL, secret)
	case "sonarqube":
		return discoverSonarQube(ctx, client, baseURL, secret)
	case "harbor_trivy":
		return discoverHarbor(ctx, client, baseURL, secret)
	case "sentry":
		return discoverSentry(ctx, client, baseURL, secret)
	default:
		return nil, fmt.Errorf("integrations: unknown provider %q", provider)
	}
}

const discoverMaxPages = 10 // 100/page => up to 1000 resources per connection

// BindingKindForProvider maps a Connection.Provider to the metric_bindings
// .kind value discovering through it should produce — identical for three
// of the four, but connections.provider is "harbor_trivy" (the CHECK
// constraint's own value) while the binding kind is "trivy" (task 2.3's
// table). Centralized here so discovered_resources rows and the bindings
// created from claiming them always agree on which kind they mean.
func BindingKindForProvider(provider string) (string, error) {
	switch provider {
	case "gitlab", "sonarqube", "sentry":
		return provider, nil
	case "harbor_trivy":
		return "trivy", nil
	default:
		return "", fmt.Errorf("integrations: unknown provider %q", provider)
	}
}

func discoverGitLab(ctx context.Context, client *http.Client, baseURL, secret string) ([]DiscoveredItem, error) {
	var items []DiscoveredItem
	for page := 1; page <= discoverMaxPages; page++ {
		resp, err := get(ctx, client,
			fmt.Sprintf("%s/api/v4/projects?membership=true&per_page=100&page=%d", baseURL, page),
			func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", secret) })
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return nil, fmt.Errorf("integrations: GitLab rejected the credential (HTTP %d)", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("integrations: GitLab returned HTTP %d", resp.StatusCode)
		}
		var batch []struct {
			PathWithNamespace string `json:"path_with_namespace"`
			Name              string `json:"name"`
			ID                int    `json:"id"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&batch)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, p := range batch {
			items = append(items, DiscoveredItem{
				ExternalKey: p.PathWithNamespace, Label: p.Name,
				Metadata: map[string]any{"gitlab_project_id": p.ID},
			})
		}
		if len(batch) < 100 {
			break
		}
	}
	return items, nil
}

// discoverSonarQube returns DiscoverDegradedNoAdminScope (not an error the
// caller should surface as a failure) when the token can authenticate but
// /api/projects/search comes back 403 — a real, common shape for a
// SonarQube analysis-only token.
func discoverSonarQube(ctx context.Context, client *http.Client, baseURL, secret string) ([]DiscoveredItem, error) {
	resp, err := get(ctx, client, baseURL+"/api/projects/search?ps=500", func(r *http.Request) {
		r.SetBasicAuth(secret, "")
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, DiscoverDegradedNoAdminScope
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("integrations: SonarQube rejected the credential")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("integrations: SonarQube returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Components []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	items := make([]DiscoveredItem, 0, len(body.Components))
	for _, c := range body.Components {
		items = append(items, DiscoveredItem{ExternalKey: c.Key, Label: c.Name})
	}
	return items, nil
}

// discoverHarbor lists every accessible project, then every repository in
// each — the exact call ("ListRepositories(project)") the spec says the
// sync loop already needs, feeding the discovery picker rather than
// something built new for it. Harbor's own repository "name" field is
// already "project/repo"-shaped, which is exactly task 2.3's external_key
// format for the trivy kind.
func discoverHarbor(ctx context.Context, client *http.Client, baseURL, secret string) ([]DiscoveredItem, error) {
	authHeader := func(r *http.Request) {
		user, pass, ok := strings.Cut(secret, ":")
		if !ok {
			user, pass = secret, ""
		}
		r.SetBasicAuth(user, pass)
	}

	projResp, err := get(ctx, client, baseURL+"/api/v2.0/projects?page_size=100", authHeader)
	if err != nil {
		return nil, err
	}
	defer projResp.Body.Close()
	if projResp.StatusCode == http.StatusUnauthorized || projResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("integrations: Harbor rejected the credential (HTTP %d)", projResp.StatusCode)
	}
	if projResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("integrations: Harbor returned HTTP %d", projResp.StatusCode)
	}
	var projects []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(projResp.Body).Decode(&projects); err != nil {
		return nil, err
	}

	var items []DiscoveredItem
	for _, proj := range projects {
		for page := 1; page <= discoverMaxPages; page++ {
			repoResp, err := get(ctx, client,
				fmt.Sprintf("%s/api/v2.0/projects/%s/repositories?page_size=100&page=%d", baseURL, url.PathEscape(proj.Name), page),
				authHeader)
			if err != nil {
				return nil, err
			}
			if repoResp.StatusCode != http.StatusOK {
				repoResp.Body.Close()
				break // this project's repos aren't listable (permissions); move on rather than fail the whole discovery
			}
			var repos []struct {
				Name string `json:"name"`
			}
			decodeErr := json.NewDecoder(repoResp.Body).Decode(&repos)
			repoResp.Body.Close()
			if decodeErr != nil {
				return nil, decodeErr
			}
			for _, r := range repos {
				items = append(items, DiscoveredItem{ExternalKey: r.Name, Label: r.Name})
			}
			if len(repos) < 100 {
				break
			}
		}
	}
	return items, nil
}

// discoverSentry lists projects visible to the token. The spec also asks
// for previously-seen service_name tag values (generalizing the old
// Lotus's sentry_service_tags) — that needs paginating each project's
// issue tag-values, real collector work rather than a discovery-time
// listing call, so it's left for Fase 3 and logged as a TENSION rather
// than half-built here.
func discoverSentry(ctx context.Context, client *http.Client, baseURL, secret string) ([]DiscoveredItem, error) {
	resp, err := get(ctx, client, baseURL+"/api/0/projects/", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("integrations: Sentry rejected the credential (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("integrations: Sentry returned HTTP %d", resp.StatusCode)
	}
	var projects []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, err
	}
	items := make([]DiscoveredItem, 0, len(projects))
	for _, p := range projects {
		items = append(items, DiscoveredItem{ExternalKey: p.Slug, Label: p.Name})
	}
	return items, nil
}
