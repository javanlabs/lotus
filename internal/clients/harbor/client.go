// Package harbor talks to a Harbor v2 registry's REST API. Ported from the
// legacy Lotus (/Users/uyab/Code/lotus/web/internal/clients/harbor) as-is
// — ListRepositories, LatestArtifactWithScan, Summarize, GetVulnerabilities
// are unchanged. New for the SaaS port: New takes an injected
// *http.Client, get classifies status codes into *StatusError, Ping gives
// Test Connection its call, and FindArtifact adds tag-policy support
// (task 3.4's binding-level "latest_scanned vs a fixed tag" — the legacy
// tool only ever used the latest, since it had no per-binding config at
// all).
//
// Endpoints used (under /api/v2.0):
//   - GET /projects/{project}/repositories?page_size=100
//   - GET /projects/{project}/repositories/{repo_name}/artifacts?with_scan_overview=true&page_size=20&sort=-push_time
//
// Auth: HTTP Basic with username/password (or robot account name + token).
package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func New(baseURL, username, password string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), username: username, password: password, http: httpClient}
}

// StatusError mirrors internal/clients/gitlab's — see that package's doc
// comment.
type StatusError struct {
	Path       string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("harbor GET %s: %d %s", e.Path, e.StatusCode, e.Body)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		se := &StatusError{Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if resp.StatusCode == http.StatusTooManyRequests {
			se.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return se
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return time.Minute
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return time.Minute
}

// Repository is one repo under a project.
type Repository struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"` // "alurkerja-v2/alurkerja-bpm-release"
	ArtifactCount int       `json:"artifact_count"`
	PullCount     int       `json:"pull_count"`
	UpdateTime    time.Time `json:"update_time"`
}

// Project is one Harbor project (a namespace repositories live under).
type Project struct {
	Name string `json:"name"`
}

// ListProjects returns every project this credential can see, paginating
// until exhausted. Not in the legacy client — the legacy tool always
// scanned one hardcoded HARBOR_PROJECT; task 2.4/3.4's Discover needs to
// enumerate them all.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var all []Project
	page := 1
	for {
		var batch []Project
		path := fmt.Sprintf("/api/v2.0/projects?page=%d&page_size=100", page)
		if err := c.get(ctx, path, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		page++
		if page > 50 {
			break
		}
	}
	return all, nil
}

// ListRepositories returns repos in a project, paginating until exhausted.
func (c *Client) ListRepositories(ctx context.Context, project string) ([]Repository, error) {
	var all []Repository
	page := 1
	for {
		var batch []Repository
		path := fmt.Sprintf("/api/v2.0/projects/%s/repositories?page=%d&page_size=100",
			url.PathEscape(project), page)
		if err := c.get(ctx, path, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		page++
		if page > 50 { // safety
			break
		}
	}
	return all, nil
}

// vulnSummary mirrors the nested shape Harbor returns inside scan_overview.
type vulnSummary struct {
	Severity   string `json:"severity"`
	ScanStatus string `json:"scan_status"`
	Summary    struct {
		Total   int            `json:"total"`
		Fixable int            `json:"fixable"`
		Summary map[string]int `json:"summary"` // {"Critical":N,"High":N,...}
	} `json:"summary"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}

// Tag represents one tag on an artifact.
type Tag struct {
	Name     string    `json:"name"`
	PushTime time.Time `json:"push_time"`
	PullTime time.Time `json:"pull_time"`
}

// Artifact is the response shape we care about.
type Artifact struct {
	Digest       string                 `json:"digest"`
	Type         string                 `json:"type"`
	PushTime     time.Time              `json:"push_time"`
	Tags         []Tag                  `json:"tags"`
	ScanOverview map[string]vulnSummary `json:"scan_overview"`
}

// LatestArtifactWithScan returns the most recently pushed artifact in repoName
// (after the project prefix) that has a finished scan report. If none has one
// yet, the newest artifact is returned anyway with no report.
func (c *Client) LatestArtifactWithScan(ctx context.Context, project, repoName string) (*Artifact, error) {
	arts, err := c.listArtifacts(ctx, project, repoName)
	if err != nil {
		return nil, err
	}
	if len(arts) == 0 {
		return nil, nil
	}
	for i := range arts {
		if Summarize(&arts[i]).HasReport {
			return &arts[i], nil
		}
	}
	return &arts[0], nil
}

// FindArtifact implements task 3.4's binding-level tag policy: an empty
// tagPattern means "latest scanned" (LatestArtifactWithScan's existing
// behavior); a non-empty one looks for an artifact carrying that exact
// tag name instead. Not in the legacy client — the legacy tool had no
// per-binding config to express this with at all.
// FindArtifact returns the newest artifact whose tag matches tagPattern,
// or the newest scanned artifact when the pattern is empty.
//
// The pattern is a glob (path.Match syntax: *, ?, [...]), which is what
// the binding field has always been called and what a tag like
// "release-*" or "v1.*" asks for. A pattern with no metacharacters still
// matches exactly, so every binding written against the old exact-match
// behaviour keeps resolving to the same image.
func (c *Client) FindArtifact(ctx context.Context, project, repoName, tagPattern string) (*Artifact, error) {
	if tagPattern == "" {
		return c.LatestArtifactWithScan(ctx, project, repoName)
	}
	if _, err := path.Match(tagPattern, ""); err != nil {
		// Reported rather than swallowed: a malformed pattern would
		// otherwise match nothing and read as "this image has no tags",
		// which sends the reader looking in Harbor instead of at the
		// binding they mistyped.
		return nil, fmt.Errorf("harbor: invalid tag pattern %q: %w", tagPattern, err)
	}
	arts, err := c.listArtifacts(ctx, project, repoName)
	if err != nil {
		return nil, err
	}
	// Artifacts arrive newest-first (sort=-push_time), so the first match
	// is the most recent image whose tag matches.
	for i := range arts {
		for _, tag := range arts[i].Tags {
			if ok, _ := path.Match(tagPattern, tag.Name); ok {
				return &arts[i], nil
			}
		}
	}
	return nil, nil
}

func (c *Client) listArtifacts(ctx context.Context, project, repoName string) ([]Artifact, error) {
	// Harbor wants the part after "<project>/" as the repository_name.
	short := strings.TrimPrefix(repoName, project+"/")
	short = url.PathEscape(short)
	path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts?with_scan_overview=true&page_size=20&sort=-push_time",
		url.PathEscape(project), short)
	var arts []Artifact
	if err := c.get(ctx, path, &arts); err != nil {
		return nil, err
	}
	return arts, nil
}

// ScanSummary flattens the first vulnerability report Harbor exposes.
type ScanSummary struct {
	Severity   string
	Total      int
	Fixable    int
	Critical   int
	High       int
	Medium     int
	Low        int
	Unknown    int
	StartTime  *time.Time
	EndTime    *time.Time
	HasReport  bool
	RawSummary string
}

// Vulnerability is one CVE entry from a Trivy report.
type Vulnerability struct {
	ID          string   `json:"id"`
	Package     string   `json:"package"`
	Version     string   `json:"version"`
	FixVersion  string   `json:"fix_version"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Links       []string `json:"links"`
	CweIDs      []string `json:"cwe_ids"`
}

// VulnerabilityReport is the full report shape returned by the additions endpoint.
type VulnerabilityReport struct {
	GeneratedAt     *time.Time      `json:"generated_at,omitempty"`
	Severity        string          `json:"severity"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
	Scanner         struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"scanner"`
}

// GetVulnerabilities fetches the full Trivy report for one artifact reference
// (digest or tag). Harbor returns a MIME-keyed object, so we pick whichever
// vulnerability-report variant the server emits.
func (c *Client) GetVulnerabilities(ctx context.Context, project, repoName, ref string) (*VulnerabilityReport, error) {
	short := strings.TrimPrefix(repoName, project+"/")
	short = url.PathEscape(short)
	path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts/%s/additions/vulnerabilities",
		url.PathEscape(project), short, url.PathEscape(ref))
	raw := map[string]json.RawMessage{}
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	for k, v := range raw {
		if !strings.Contains(k, "vulnerability") {
			continue
		}
		var rep VulnerabilityReport
		if err := json.Unmarshal(v, &rep); err != nil {
			return nil, fmt.Errorf("decode vuln report: %w", err)
		}
		return &rep, nil
	}
	return nil, nil
}

// Summarize picks a vulnerability report from scan_overview (any key, since
// Harbor uses long MIME-type keys like "application/vnd.security.vulnerability.report; version=1.1").
func Summarize(a *Artifact) ScanSummary {
	out := ScanSummary{}
	if a == nil || len(a.ScanOverview) == 0 {
		return out
	}
	var picked vulnSummary
	for _, v := range a.ScanOverview {
		picked = v
		break
	}
	// A queued, running or failed scan also has an overview entry, with counts
	// of zero that are not a result. Only a finished one is a report.
	if picked.ScanStatus != "" && !strings.EqualFold(picked.ScanStatus, "Success") {
		return out
	}
	out.HasReport = true
	out.Severity = picked.Severity
	out.Total = picked.Summary.Total
	out.Fixable = picked.Summary.Fixable
	out.Critical = picked.Summary.Summary["Critical"]
	out.High = picked.Summary.Summary["High"]
	out.Medium = picked.Summary.Summary["Medium"]
	out.Low = picked.Summary.Summary["Low"]
	out.Unknown = picked.Summary.Summary["Unknown"] + picked.Summary.Summary["None"]
	out.StartTime = picked.StartTime
	out.EndTime = picked.EndTime
	if b, err := json.Marshal(picked); err == nil {
		out.RawSummary = string(b)
	}
	return out
}

// Ping is Test Connection's call — Harbor's own "who am I" endpoint.
func (c *Client) Ping(ctx context.Context) error {
	var whoami struct {
		Username string `json:"username"`
	}
	return c.get(ctx, "/api/v2.0/users/current", &whoami)
}
