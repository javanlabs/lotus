// Package gitlab is task 3.3's GitLab client, ported from the legacy
// Lotus (/Users/uyab/Code/lotus/web/internal/clients/gitlab) as-is —
// FetchHealth's shape, its 6-call orchestration, and its best-effort
// sub-call semantics are unchanged. What's new for the SaaS port: New
// takes an injected *http.Client (task 3.2's per-connection rate limiter
// lives on its Transport, not something this package knows about), get
// classifies HTTP status codes into *StatusError so the collector layer
// (internal/collector/gitlab) can map them to task 3.1's error classes,
// and ListProjects feeds task 2.4/3.1's Discover.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal GitLab REST v4 client. Auth uses the PRIVATE-TOKEN
// header (a personal/project access token).
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client against an already-configured http.Client — the
// caller (internal/collector/gitlab.Open) is responsible for its
// Transport carrying the SSRF guard (task 2.2) and rate limiter (task
// 3.2); this package has no opinion on either.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

// StatusError carries the HTTP status code a request failed with, so
// callers can classify it (401/403 → auth, 404 → not found, 429 → rate
// limited with RetryAfter, 5xx → upstream) without re-parsing an error
// string.
type StatusError struct {
	Path       string
	StatusCode int
	Body       string
	RetryAfter time.Duration // parsed from Retry-After, only set for 429
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gitlab GET %s: %d %s", e.Path, e.StatusCode, e.Body)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v4"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
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
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return time.Minute // GitLab's own default backoff window when unspecified
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return time.Minute
}

// pid URL-encodes a namespaced project path ("group/sub/repo") so it can be
// used as a GitLab project id (slashes become %2F).
func pid(gitlabPath string) string {
	return url.QueryEscape(gitlabPath)
}

// --- GitLab API response shapes (only the fields we use) ---

type project struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	PathWithNS     string    `json:"path_with_namespace"`
	DefaultBranch  string    `json:"default_branch"`
	Visibility     string    `json:"visibility"`
	Archived       bool      `json:"archived"`
	LastActivityAt time.Time `json:"last_activity_at"`
	WebURL         string    `json:"web_url"`
}

type branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		CommittedDate time.Time `json:"committed_date"`
	} `json:"commit"`
}

type pipeline struct {
	ID         int        `json:"id"`
	Status     string     `json:"status"`
	Ref        string     `json:"ref"`
	WebURL     string     `json:"web_url"`
	Duration   *int       `json:"duration"` // seconds, null until finished
	FinishedAt *time.Time `json:"finished_at"`
}

type commit struct {
	ID            string `json:"id"`
	CommitterName string `json:"committer_name"`
	AuthorName    string `json:"author_name"`
}

// Health is the aggregated per-repo snapshot returned by FetchHealth.
type Health struct {
	PrimaryLanguage string
	LanguagesJSON   string
	DefaultBranch   string

	BranchesTotal  int
	BranchesActive int
	BranchesStale  int

	DefaultBranchProtected bool
	Visibility             string
	Archived               bool
	LastActivityAt         *time.Time
	WebURL                 string

	Commits30d        int
	Commits30dCapped  bool
	LastCommitterName string

	LastPipelineStatus      string
	LastPipelineID          *int
	LastPipelineDurationSec *int
	LastPipelineWebURL      string
	LastPipelineFinishedAt  *time.Time
	LastPipelineRef         string

	PipelineSuccessRate *float64
	PipelineSampleSize  int

	// LastFinishedPipelineStatus is the status of the newest pipeline in
	// the sample that has finished (success, failed or canceled), "" when
	// none has. LastPipelineStatus may be running or pending, which has no
	// verdict yet; the score's failed-pipeline cap reads this instead.
	LastFinishedPipelineStatus string

	// Warnings records which of the five best-effort sub-calls (below the
	// fatal project lookup) failed and why — task 3.1's rule made
	// concrete: the caller (internal/collector/gitlab) turns each of
	// these into a collector.Warning and omits the corresponding Fact,
	// rather than writing a zero-value one.
	Warnings []SubCallFailure
}

// SubCallFailure.Err is a string, not error: Health (and this with it) is
// JSON round-tripped as the collector.Result's Detail blob (task 3.1) and
// then read back on the persistence path — an error-typed field can be
// marshaled but can never be unmarshaled back into a concrete error, so a
// plain message string is what actually survives the round trip.
type SubCallFailure struct {
	Step string
	Err  string
}

// FetchHealth orchestrates several GitLab API calls for one repo into a single
// Health snapshot. Sub-calls (languages, branches, pipelines, commits) are
// best-effort: a failure in one does not abort the whole fetch — only the
// project lookup is fatal (it confirms the repo exists & is reachable).
// HealthOptions are the per-binding knobs FetchHealth reads. A zero value
// means "use the documented default", so a binding written before a knob
// existed behaves exactly as it did.
type HealthOptions struct {
	DefaultBranch      string
	StaleDays          int
	PipelineSampleSize int // pipelines examined for the success rate
	CommitWindowDays   int // how far back the commit-velocity count looks
}

func (o HealthOptions) withDefaults() HealthOptions {
	if o.StaleDays <= 0 {
		o.StaleDays = 30
	}
	if o.PipelineSampleSize <= 0 {
		o.PipelineSampleSize = 20
	}
	if o.CommitWindowDays <= 0 {
		o.CommitWindowDays = 30
	}
	return o
}

// PipelineFinished is whether a pipeline status is a final verdict.
// Running, pending, created, manual, scheduled and skipped are not: a
// pipeline in flight has not failed.
func PipelineFinished(status string) bool {
	switch status {
	case "success", "failed", "canceled":
		return true
	}
	return false
}

func (c *Client) FetchHealth(ctx context.Context, gitlabPath string, opts HealthOptions) (*Health, error) {
	opts = opts.withDefaults()
	defaultBranch, staleDays := opts.DefaultBranch, opts.StaleDays
	id := pid(gitlabPath)
	h := &Health{}

	// 1. Project metadata (fatal on error).
	var p project
	if err := c.get(ctx, "/projects/"+id, &p); err != nil {
		return nil, err
	}
	h.DefaultBranch = p.DefaultBranch
	if h.DefaultBranch == "" {
		h.DefaultBranch = defaultBranch
	}
	h.Visibility = p.Visibility
	h.Archived = p.Archived
	h.WebURL = p.WebURL
	if !p.LastActivityAt.IsZero() {
		t := p.LastActivityAt
		h.LastActivityAt = &t
	}

	// 2. Languages → primary + full breakdown.
	var langs map[string]float64
	if err := c.get(ctx, "/projects/"+id+"/languages", &langs); err != nil {
		h.Warnings = append(h.Warnings, SubCallFailure{Step: "languages", Err: err.Error()})
	} else if len(langs) > 0 {
		h.PrimaryLanguage = topLang(langs)
		if b, e := json.Marshal(langs); e == nil {
			h.LanguagesJSON = string(b)
		}
	}

	// 3. Branches (paginated) → total / active / stale + default-branch protection.
	branches, err := c.allBranches(ctx, id)
	if err != nil {
		h.Warnings = append(h.Warnings, SubCallFailure{Step: "branches", Err: err.Error()})
	}
	cutoff := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour)
	for _, b := range branches {
		h.BranchesTotal++
		if b.Commit.CommittedDate.After(cutoff) {
			h.BranchesActive++
		} else {
			h.BranchesStale++
		}
		if b.Name == h.DefaultBranch {
			h.DefaultBranchProtected = b.Protected
		}
	}

	// 4+5. Pipelines on default branch → success rate + latest pipeline detail.
	var pls []pipeline
	if err := c.get(ctx, fmt.Sprintf("/projects/%s/pipelines?ref=%s&per_page=%d",
		id, url.QueryEscape(h.DefaultBranch), opts.PipelineSampleSize), &pls); err != nil {
		h.Warnings = append(h.Warnings, SubCallFailure{Step: "pipelines", Err: err.Error()})
	} else if len(pls) > 0 {
		success := 0
		for _, pl := range pls {
			if pl.Status == "success" {
				success++
			}
		}
		h.PipelineSampleSize = len(pls)
		rate := float64(success) / float64(len(pls))
		h.PipelineSuccessRate = &rate
		for _, pl := range pls {
			if PipelineFinished(pl.Status) {
				h.LastFinishedPipelineStatus = pl.Status
				break
			}
		}

		latest := pls[0]
		h.LastPipelineStatus = latest.Status
		h.LastPipelineRef = latest.Ref
		h.LastPipelineWebURL = latest.WebURL
		lid := latest.ID
		h.LastPipelineID = &lid

		var pd pipeline
		if err := c.get(ctx, "/projects/"+id+"/pipelines/"+strconv.Itoa(latest.ID), &pd); err == nil {
			h.LastPipelineDurationSec = pd.Duration
			h.LastPipelineFinishedAt = pd.FinishedAt
			if pd.Status != "" {
				h.LastPipelineStatus = pd.Status
			}
		}
	}

	// 6. Commits in the configured window on the default branch (velocity).
	since := time.Now().AddDate(0, 0, -opts.CommitWindowDays).UTC().Format(time.RFC3339)
	var commits []commit
	if err := c.get(ctx, "/projects/"+id+"/repository/commits?ref_name="+url.QueryEscape(h.DefaultBranch)+"&since="+url.QueryEscape(since)+"&per_page=100", &commits); err != nil {
		h.Warnings = append(h.Warnings, SubCallFailure{Step: "commits", Err: err.Error()})
	} else {
		h.Commits30d = len(commits)
		if len(commits) >= 100 {
			h.Commits30dCapped = true
		}
	}

	// Latest commit on the default branch → committer name (no since filter so
	// it's available even on quiet repos). Folded into the same "commits"
	// warning step as #6 above rather than a distinct one — both fail for
	// the same underlying reason (the commits endpoint being unreachable).
	var latest []commit
	if err := c.get(ctx, "/projects/"+id+"/repository/commits?ref_name="+url.QueryEscape(h.DefaultBranch)+"&per_page=1", &latest); err == nil && len(latest) > 0 {
		h.LastCommitterName = latest[0].CommitterName
		if h.LastCommitterName == "" {
			h.LastCommitterName = latest[0].AuthorName
		}
	}

	return h, nil
}

// allBranches pages through the branches endpoint (100/page, capped at 10
// pages = 1000 branches to bound API cost on pathological repos).
func (c *Client) allBranches(ctx context.Context, id string) ([]branch, error) {
	var all []branch
	for page := 1; page <= 10; page++ {
		var batch []branch
		path := fmt.Sprintf("/projects/%s/repository/branches?per_page=100&page=%d", id, page)
		if err := c.get(ctx, path, &batch); err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func topLang(langs map[string]float64) string {
	best := ""
	var bestPct float64
	for k, v := range langs {
		if v > bestPct {
			bestPct, best = v, k
		}
	}
	return best
}

// ProjectSummary is one entry from ListProjects — task 2.4/3.1's Discover.
type ProjectSummary struct {
	ID                int
	Name              string
	PathWithNamespace string
}

// ListProjects pages through /projects?membership=true (up to 1000
// projects, same bound as allBranches) — the identical call task 2.4's
// discoverGitLab already made ad hoc; this is its Collector-interface
// home.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	var out []ProjectSummary
	for page := 1; page <= 10; page++ {
		var batch []project
		path := fmt.Sprintf("/projects?membership=true&per_page=100&page=%d", page)
		if err := c.get(ctx, path, &batch); err != nil {
			return out, err
		}
		for _, p := range batch {
			out = append(out, ProjectSummary{ID: p.ID, Name: p.Name, PathWithNamespace: p.PathWithNS})
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// Ping is the cheapest authenticated call GitLab offers — task 2.2/3.1's
// Test Connection / Session.Ping.
func (c *Client) Ping(ctx context.Context) error {
	var whoami struct {
		Username string `json:"username"`
	}
	return c.get(ctx, "/user", &whoami)
}

// ProjectExists is task 2.3/3.1's Test Binding / Session.Validate — a
// lookup with no side effects, reusing the same fatal-on-error path
// FetchHealth's first call does.
func (c *Client) ProjectExists(ctx context.Context, gitlabPath string) (name string, err error) {
	var p project
	if err := c.get(ctx, "/projects/"+pid(gitlabPath), &p); err != nil {
		return "", err
	}
	return p.PathWithNS, nil
}
