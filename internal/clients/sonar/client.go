// Package sonar is task 3.3's SonarQube client, ported from the legacy
// Lotus (/Users/uyab/Code/lotus/web/internal/clients/sonar) as-is —
// Fetch's three-call shape, ratingToGrade, parseLanguages, and
// SearchIssues are unchanged. New for the SaaS port: New takes an
// injected *http.Client, get classifies status codes into *StatusError,
// and Ping/ProjectExists give the collector layer
// (internal/collector/sonarqube) its Test-Connection/Test-Binding calls.
package sonar

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

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

// StatusError mirrors internal/clients/gitlab's — see that package's doc
// comment for why this exists (status-code classification for the
// collector layer, not string matching).
type StatusError struct {
	Path       string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("sonar GET %s: %d %s", e.Path, e.StatusCode, e.Body)
}

// Sonar uses HTTP Basic auth with token as username and empty password.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.token, "")
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
		return time.Minute
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return time.Minute
}

type Measures struct {
	QualityGate            string
	Coverage               *float64
	Bugs                   *int
	Vulnerabilities        *int
	CodeSmells             *int
	SecurityHotspots       *int
	DuplicatedLinesDensity *float64
	NCLOC                  *int
	Complexity             *int
	ReliabilityRating      string // grade A..E
	SecurityRating         string
	SqaleRating            string
	SqaleIndexMin          *int   // minutes of technical debt
	TestExecutionTimeMs    *int   // total unit-test execution duration, ms
	Languages              string // "Go, TS"
	LastScan               *time.Time
	RawJSON                string
}

// WorstRating returns the worst (highest-severity) of the three ratings —
// task 3.3's headline grade: "terburuk dari rating reliability / security
// / maintainability". Grades are single letters A (best) through E
// (worst), so a plain lexical max is exactly the severity ordering; an
// empty/unset rating is skipped rather than treated as worst.
func (m Measures) WorstRating() string {
	worst := ""
	for _, r := range []string{m.ReliabilityRating, m.SecurityRating, m.SqaleRating} {
		if r != "" && r > worst {
			worst = r
		}
	}
	return worst
}

var metricKeys = []string{
	"ncloc", "security_hotspots", "reliability_rating", "security_rating",
	"sqale_rating", "security_hotspots_reviewed", "sqale_index", "vulnerabilities",
	"complexity", "ncloc_language_distribution", "bugs", "code_smells",
	"coverage", "duplicated_lines_density", "test_execution_time",
}

type measuresResp struct {
	Component struct {
		Measures []struct {
			Metric string `json:"metric"`
			Value  string `json:"value"`
		} `json:"measures"`
	} `json:"component"`
}

type qualityGateResp struct {
	ProjectStatus struct {
		Status string `json:"status"`
	} `json:"projectStatus"`
}

type analysesResp struct {
	Analyses []struct {
		Date string `json:"date"`
	} `json:"analyses"`
}

// Fetch returns the combined measures + quality gate + last scan for a project.
// branch is optional; when empty the main branch is used.
func (c *Client) Fetch(ctx context.Context, projectKey, branch string) (*Measures, error) {
	branchQS := ""
	if branch != "" {
		branchQS = "&branch=" + url.QueryEscape(branch)
	}

	measurePath := fmt.Sprintf(
		"/api/measures/component?component=%s&metricKeys=%s%s",
		url.QueryEscape(projectKey),
		strings.Join(metricKeys, ","),
		branchQS,
	)

	var mr measuresResp
	if err := c.get(ctx, measurePath, &mr); err != nil {
		return nil, fmt.Errorf("measures: %w", err)
	}

	m := &Measures{}
	raw := map[string]string{}
	for _, it := range mr.Component.Measures {
		raw[it.Metric] = it.Value
		switch it.Metric {
		case "coverage":
			m.Coverage = parseFloat(it.Value)
		case "duplicated_lines_density":
			m.DuplicatedLinesDensity = parseFloat(it.Value)
		case "bugs":
			m.Bugs = parseInt(it.Value)
		case "vulnerabilities":
			m.Vulnerabilities = parseInt(it.Value)
		case "code_smells":
			m.CodeSmells = parseInt(it.Value)
		case "security_hotspots":
			m.SecurityHotspots = parseInt(it.Value)
		case "ncloc":
			m.NCLOC = parseInt(it.Value)
		case "complexity":
			m.Complexity = parseInt(it.Value)
		case "sqale_index":
			m.SqaleIndexMin = parseInt(it.Value)
		case "test_execution_time":
			m.TestExecutionTimeMs = parseInt(it.Value)
		case "reliability_rating":
			m.ReliabilityRating = ratingToGrade(it.Value)
		case "security_rating":
			m.SecurityRating = ratingToGrade(it.Value)
		case "sqale_rating":
			m.SqaleRating = ratingToGrade(it.Value)
		case "ncloc_language_distribution":
			m.Languages = parseLanguages(it.Value)
		}
	}

	rawBytes, _ := json.Marshal(raw)
	m.RawJSON = string(rawBytes)

	var gr qualityGateResp
	gatePath := fmt.Sprintf("/api/qualitygates/project_status?projectKey=%s%s", url.QueryEscape(projectKey), branchQS)
	if err := c.get(ctx, gatePath, &gr); err == nil {
		m.QualityGate = gr.ProjectStatus.Status
	}

	var ar analysesResp
	analysesPath := fmt.Sprintf("/api/project_analyses/search?project=%s&ps=1", url.QueryEscape(projectKey))
	if branch != "" {
		analysesPath += "&branch=" + url.QueryEscape(branch)
	}
	if err := c.get(ctx, analysesPath, &ar); err == nil && len(ar.Analyses) > 0 {
		if t, err := time.Parse(time.RFC3339, ar.Analyses[0].Date); err == nil {
			m.LastScan = &t
		} else if t, err := time.Parse("2006-01-02T15:04:05-0700", ar.Analyses[0].Date); err == nil {
			m.LastScan = &t
		}
	}

	return m, nil
}

// Issue is a single Sonar issue (bug, vulnerability, or code smell).
type Issue struct {
	Key       string
	Rule      string
	Severity  string // BLOCKER, CRITICAL, MAJOR, MINOR, INFO
	Type      string // BUG, VULNERABILITY, CODE_SMELL
	Component string
	Line      int
	Message   string
	Effort    string // e.g. "30min"
	Status    string
}

type issuesResp struct {
	Issues []struct {
		Key       string `json:"key"`
		Rule      string `json:"rule"`
		Severity  string `json:"severity"`
		Type      string `json:"type"`
		Component string `json:"component"`
		Line      int    `json:"line"`
		Message   string `json:"message"`
		Effort    string `json:"effort"`
		Status    string `json:"status"`
	} `json:"issues"`
	Paging struct {
		Total int `json:"total"`
	} `json:"paging"`
}

// SearchIssues lists open issues for a project, severities optional (comma-separated like
// "BLOCKER,CRITICAL,MAJOR"). limit caps the number returned (Sonar maxes ps=500).
func (c *Client) SearchIssues(ctx context.Context, projectKey, branch, severities string, limit int) ([]Issue, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := url.Values{}
	q.Set("componentKeys", projectKey)
	q.Set("resolved", "false")
	q.Set("s", "SEVERITY")
	q.Set("asc", "false")
	q.Set("ps", strconv.Itoa(limit))
	if severities != "" {
		q.Set("severities", severities)
	}
	if branch != "" {
		q.Set("branch", branch)
	}
	var resp issuesResp
	if err := c.get(ctx, "/api/issues/search?"+q.Encode(), &resp); err != nil {
		return nil, 0, err
	}
	out := make([]Issue, 0, len(resp.Issues))
	for _, it := range resp.Issues {
		out = append(out, Issue{
			Key: it.Key, Rule: it.Rule, Severity: it.Severity, Type: it.Type,
			Component: it.Component, Line: it.Line, Message: it.Message, Effort: it.Effort, Status: it.Status,
		})
	}
	return out, resp.Paging.Total, nil
}

var langNames = map[string]string{
	"go": "Go", "java": "Java", "ts": "TS", "js": "JS",
	"py": "Python", "php": "PHP", "kotlin": "Kotlin",
}

func parseLanguages(raw string) string {
	type lc struct {
		name  string
		lines int
	}
	var list []lc
	for _, part := range strings.Split(raw, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		name, ok := langNames[strings.ToLower(strings.TrimSpace(kv[0]))]
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(kv[1]))
		list = append(list, lc{name, n})
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].lines > list[j-1].lines; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	out := make([]string, 0, len(list))
	for _, l := range list {
		out = append(out, l.name)
	}
	return strings.Join(out, ", ")
}

func ratingToGrade(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	n := int(f)
	if n < 1 || n > 5 {
		return ""
	}
	return string(rune('A' + n - 1))
}

func parseFloat(s string) *float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

func parseInt(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

// Ping is Test Connection's call — /api/authentication/validate is
// SonarQube's own purpose-built "is this token good" endpoint.
func (c *Client) Ping(ctx context.Context) error {
	var v struct {
		Valid bool `json:"valid"`
	}
	if err := c.get(ctx, "/api/authentication/validate", &v); err != nil {
		return err
	}
	if !v.Valid {
		return &StatusError{Path: "/api/authentication/validate", StatusCode: http.StatusUnauthorized, Body: "token rejected"}
	}
	return nil
}

// ProjectExists is Test Binding's call.
func (c *Client) ProjectExists(ctx context.Context, projectKey string) (name string, err error) {
	var resp struct {
		Component struct {
			Name string `json:"name"`
		} `json:"component"`
	}
	if err := c.get(ctx, "/api/components/show?component="+url.QueryEscape(projectKey), &resp); err != nil {
		return "", err
	}
	return resp.Component.Name, nil
}

// ProjectSummary is one entry from ListProjects.
type ProjectSummary struct {
	Key  string
	Name string
}

// ListProjects is Discover's call — /api/projects/search. Callers should
// treat a 403 here as task 2.4/3.3's documented degrade-to-free-text case
// (an analysis token without admin scope), not a hard failure.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	var resp struct {
		Components []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"components"`
	}
	if err := c.get(ctx, "/api/projects/search?ps=500", &resp); err != nil {
		return nil, err
	}
	out := make([]ProjectSummary, 0, len(resp.Components))
	for _, c := range resp.Components {
		out = append(out, ProjectSummary{Key: c.Key, Name: c.Name})
	}
	return out, nil
}
