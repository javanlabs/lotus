// Package sentry is a thin read-only client for the Sentry Web API (v0),
// covering only what the dashboard needs: project metadata, the list of
// environments, and the issue stream. Ported from the legacy Lotus
// (/Users/uyab/Code/lotus/web/internal/clients/sentry) as-is — Project,
// Environments, TagValues, Issues, TagFilterQuery, and the RFC 5988 Link
// header cursor pagination (linkNextRe/nextCursor) are unchanged.
//
// New for the SaaS port: New takes an injected *http.Client (so the
// scheduler's per-connection rate limiter and SSRF guard apply, and so
// tests can point at an httptest.Server without a real network call);
// get classifies HTTP failures into *StatusError instead of a plain
// fmt.Errorf, so internal/collector/sentry can tell auth/not_found/
// rate_limited/upstream apart the way task 3.1's error taxonomy requires
// (TagValues' own 404-means-empty rule now checks the typed error via
// errors.As instead of matching the error string); and Ping/ListProjects
// are new, for Test Connection (task 2.2) and Discover (task 3.1) — the
// legacy tool never needed either since it read one hardcoded project per
// service from services.yaml.
//
// Self-hosted Sentry is assumed (org slug in the path, e.g.
// https://sentry.cloud.javan.co.id/api/0/projects/javan/alurkerja-simulation-be/).
// Auth is a bearer auth token with at least project:read + event:read.
package sentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	org     string
	http    *http.Client
}

func New(baseURL, token, org string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		org:     org,
		http:    httpClient,
	}
}

// Org is the organization slug every request is scoped to.
func (c *Client) Org() string { return c.org }

// StatusError mirrors internal/clients/gitlab's — see that package's doc
// comment. Error() keeps the exact "sentry GET %s: %d %s" shape the
// ported client_test.go's TestIssuesSurfacesHTTPError already asserts on.
type StatusError struct {
	Path       string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("sentry GET %s: %d %s", e.Path, e.StatusCode, e.Body)
}

// get performs an authenticated GET and decodes the JSON body into out. It
// returns the raw Link header too, which is how Sentry paginates.
func (c *Client) get(ctx context.Context, rawURL string, out any) (linkHeader string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		se := &StatusError{Path: stripQuery(rawURL), StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		if resp.StatusCode == http.StatusTooManyRequests {
			se.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return "", se
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("sentry GET %s: decode: %w", stripQuery(rawURL), err)
	}
	return resp.Header.Get("Link"), nil
}

// stripQuery keeps error messages readable (and keeps the token-free path only).
func stripQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
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

// Ping is Test Connection's call (task 2.2) — the cheapest authenticated
// endpoint that proves both the token and the org slug are right.
func (c *Client) Ping(ctx context.Context) error {
	var org struct {
		Slug string `json:"slug"`
	}
	u := fmt.Sprintf("%s/api/0/organizations/%s/", c.baseURL, url.PathEscape(c.org))
	_, err := c.get(ctx, u, &org)
	return err
}

// ─── Project ─────────────────────────────────────────────────────────────

// Project is the subset of Sentry's project payload the dashboard stores.
type Project struct {
	ID       int64
	Slug     string
	Name     string
	Platform string
	TeamID   int64
	TeamSlug string
	Org      string
}

type projectResp struct {
	ID       string `json:"id"` // Sentry returns ids as strings
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Team     *struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"team"`
	Teams []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"teams"`
	Organization struct {
		Slug string `json:"slug"`
	} `json:"organization"`
}

// Project fetches metadata for one project slug. The numeric id it returns is
// what the dashboard's outbound links use (Sentry's issue stream filters by
// ?project=<id>, not by slug).
func (c *Client) Project(ctx context.Context, slug string) (Project, error) {
	var r projectResp
	u := fmt.Sprintf("%s/api/0/projects/%s/%s/", c.baseURL, url.PathEscape(c.org), url.PathEscape(slug))
	if _, err := c.get(ctx, u, &r); err != nil {
		return Project{}, err
	}
	p := Project{
		ID:       atoi64(r.ID),
		Slug:     r.Slug,
		Name:     r.Name,
		Platform: r.Platform,
		Org:      r.Organization.Slug,
	}
	if p.Org == "" {
		p.Org = c.org
	}
	// `team` is the legacy single-team field; newer Sentry only sends `teams`.
	switch {
	case r.Team != nil:
		p.TeamID, p.TeamSlug = atoi64(r.Team.ID), r.Team.Slug
	case len(r.Teams) > 0:
		p.TeamID, p.TeamSlug = atoi64(r.Teams[0].ID), r.Teams[0].Slug
	}
	return p, nil
}

// projectListResp is the shape of one entry under GET
// /api/0/organizations/{org}/projects/ — not the same shape Project()
// decodes, since the org-scoped list endpoint nests team/platform
// differently and doesn't need every field Project() cares about.
type projectListResp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ListProjects returns every project visible to this token within Org,
// paginating via the same Link-header cursor Issues uses. Not in the
// legacy client — the legacy tool read one hardcoded project per service
// from services.yaml; task 3.1/2.4's Discover needs to enumerate them all.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	base := fmt.Sprintf("%s/api/0/organizations/%s/projects/", c.baseURL, url.PathEscape(c.org))
	out := make([]Project, 0, 32)
	cursor := ""
	for page := 0; page < maxPageRequests; page++ {
		u := base
		if cursor != "" {
			u += "?cursor=" + url.QueryEscape(cursor)
		}
		var batch []projectListResp
		link, err := c.get(ctx, u, &batch)
		if err != nil {
			return nil, err
		}
		for _, p := range batch {
			out = append(out, Project{Slug: p.Slug, Name: p.Name, Org: c.org})
		}
		next := nextCursor(link)
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

// ─── Environments ────────────────────────────────────────────────────────

type environmentResp struct {
	Name     string `json:"name"`
	IsHidden bool   `json:"isHidden"`
}

// Environments lists the project's visible environments. An empty result is not
// an error — it just means the project has never tagged an environment, and the
// caller should fetch issues unfiltered.
func (c *Client) Environments(ctx context.Context, slug string) ([]string, error) {
	var rs []environmentResp
	u := fmt.Sprintf("%s/api/0/projects/%s/%s/environments/?visibility=visible",
		c.baseURL, url.PathEscape(c.org), url.PathEscape(slug))
	if _, err := c.get(ctx, u, &rs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rs))
	for _, e := range rs {
		if e.IsHidden || strings.TrimSpace(e.Name) == "" {
			continue
		}
		out = append(out, e.Name)
	}
	return out, nil
}

// ─── Tag values ──────────────────────────────────────────────────────────

type tagValueResp struct {
	Value string `json:"value"`
}

// TagValues lists the values Sentry has seen for one tag on a project — used to
// enumerate the `service_name` values actually reporting into a shared project,
// so the dashboard can name the ones no binding claims.
//
// A 404 means the tag has never been seen on that project (Sentry does not
// create a tag until an event carries it); that is reported as an empty list
// rather than an error, because "nobody is tagging yet" is a normal state.
func (c *Client) TagValues(ctx context.Context, slug, tagKey string) ([]string, error) {
	var rs []tagValueResp
	u := fmt.Sprintf("%s/api/0/projects/%s/%s/tags/%s/values/",
		c.baseURL, url.PathEscape(c.org), url.PathEscape(slug), url.PathEscape(tagKey))
	if _, err := c.get(ctx, u, &rs); err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if v := strings.TrimSpace(r.Value); v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// ─── Issues ──────────────────────────────────────────────────────────────

// Issue mirrors one row of Sentry's issue stream.
type Issue struct {
	ID        string
	ShortID   string
	Title     string
	Status    string
	Substatus string
	Count     int
	FirstSeen *time.Time
	LastSeen  *time.Time
	Permalink string
}

type issueResp struct {
	ID        string     `json:"id"`
	ShortID   string     `json:"shortId"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Substatus string     `json:"substatus"`
	Count     string     `json:"count"` // Sentry sends the event count as a string
	FirstSeen *time.Time `json:"firstSeen"`
	LastSeen  *time.Time `json:"lastSeen"`
	Permalink string     `json:"permalink"`
}

// TagFilterQuery narrows a Sentry search to a single tag value, e.g.
//
//	TagFilterQuery("is:unresolved", "service_name", "bpm-service")
//	→ `is:unresolved service_name:"bpm-service"`
//
// The value is always quoted: Sentry's search splits on whitespace and treats a
// bare colon as a key separator, so an unquoted value containing either would
// silently parse as something else. Embedded quotes are escaped.
func TagFilterQuery(base, tagKey, value string) string {
	value = strings.TrimSpace(value)
	if tagKey == "" || value == "" {
		return base
	}
	filter := fmt.Sprintf(`%s:"%s"`, tagKey, strings.ReplaceAll(value, `"`, `\"`))
	if base = strings.TrimSpace(base); base == "" {
		return filter
	}
	return base + " " + filter
}

// IssueQuery bounds one call to the issue stream.
type IssueQuery struct {
	ProjectSlug string
	Environment string // "" = all environments
	Query       string // Sentry search, e.g. "is:unresolved"
	StatsPeriod string // e.g. "14d"; "" leaves Sentry's default
	Limit       int    // hard cap on issues collected across pages
}

const (
	perPage         = 100 // Sentry's maximum page size
	defaultLimit    = 200
	maxPageRequests = 20 // belt-and-braces against a pagination loop
)

// Issues walks the issue stream for one project (optionally scoped to a single
// environment), following Sentry's cursor pagination until the limit is hit.
func (c *Client) Issues(ctx context.Context, q IssueQuery) ([]Issue, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	params := url.Values{}
	if q.Query != "" {
		params.Set("query", q.Query)
	}
	if q.StatsPeriod != "" {
		params.Set("statsPeriod", q.StatsPeriod)
	}
	if q.Environment != "" {
		params.Set("environment", q.Environment)
	}
	params.Set("limit", strconv.Itoa(perPage))

	base := fmt.Sprintf("%s/api/0/projects/%s/%s/issues/",
		c.baseURL, url.PathEscape(c.org), url.PathEscape(q.ProjectSlug))

	out := make([]Issue, 0, limit)
	cursor := ""
	for page := 0; page < maxPageRequests && len(out) < limit; page++ {
		p := url.Values{}
		for k, v := range params {
			p[k] = v
		}
		if cursor != "" {
			p.Set("cursor", cursor)
		}

		var rs []issueResp
		link, err := c.get(ctx, base+"?"+p.Encode(), &rs)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if r.ID == "" {
				continue
			}
			out = append(out, Issue{
				ID:        r.ID,
				ShortID:   r.ShortID,
				Title:     r.Title,
				Status:    r.Status,
				Substatus: r.Substatus,
				Count:     atoi(r.Count),
				FirstSeen: r.FirstSeen,
				LastSeen:  r.LastSeen,
				Permalink: r.Permalink,
			})
			if len(out) >= limit {
				break
			}
		}
		next := nextCursor(link)
		if next == "" || len(rs) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

// linkNextRe pulls the cursor out of Sentry's RFC 5988 Link header. Sentry
// always emits a rel="next" entry; results="false" marks the end of the stream.
var linkNextRe = regexp.MustCompile(`<[^>]*>;\s*rel="next";\s*results="(true|false)";\s*cursor="([^"]*)"`)

func nextCursor(link string) string {
	for _, m := range linkNextRe.FindAllStringSubmatch(link, -1) {
		if m[1] == "true" {
			return m[2]
		}
	}
	return ""
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
