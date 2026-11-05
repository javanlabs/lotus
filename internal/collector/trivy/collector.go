// Package trivy implements task 3.1's Collector for the pull direction
// (Harbor registry scans) and task 3.1's Ingestor for the push direction
// (CI-submitted `trivy fs`/`trivy image` reports) — both produce the same
// Result shape, differing only in Detail.Source, per task 3.4's own
// requirement.
package trivy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/clients/harbor"
	"github.com/uyab/lotus/internal/collector"
)

const (
	defaultInterval = time.Hour // registry scans run far less often than a commit
	minInterval     = 15 * time.Minute
)

func init() {
	collector.Register(collector.KindTrivy, Collector{})
	collector.RegisterPersister(collector.KindTrivy, Persist)
}

type Collector struct{}

func (Collector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		Kind: collector.KindTrivy,
		ConnectionFields: []collector.Field{
			{Key: "base_url", Label: "Harbor URL", Type: "text", Required: true},
			{Key: "username", Label: "Username (or robot account name)", Type: "text", Required: true},
			{Key: "password", Label: "Password / robot token", Type: "secret", Required: true},
		},
		BindingFields: []collector.Field{
			// external_key is "harbor_project/repository" (task 2.3's own
			// table); tag_pattern and severity_floor are the two knobs
			// task 3.4 calls out: an empty tag_pattern means "latest
			// scanned artifact", a set one pins to that exact tag.
			{Key: "tag_pattern", Label: "Tag (blank = latest scanned)", Type: "text"},
			{Key: "severity_floor", Label: "Minimum severity to report", Type: "select",
				Options: []string{"low", "medium", "high", "critical"}, Default: "high"},
		},
		BadgeStyle:      "trivy",
		DefaultInterval: defaultInterval,
		MinInterval:     minInterval,
		CostHint:        3, // list artifacts, fetch vuln report, (list repos on Discover only)
		MetricKeys: []collector.MetricKeyDecl{
			{Key: "trivy.total", Label: "Findings (total)", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "trivy.critical", Label: "Critical", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "trivy.high", Label: "High", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "trivy.medium", Label: "Medium", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "trivy.low", Label: "Low", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "trivy.fixable", Label: "Fixable", Unit: "count", ValueType: "number", Direction: "lower_better"},
		},
	}
}

func (Collector) Open(ctx context.Context, conn collector.ConnConfig) (collector.Session, error) {
	if conn.Provider == "gitlab" {
		return openRepoSession(conn)
	}
	if conn.BaseURL == "" || conn.Secret == "" {
		return nil, collector.ErrConfig(fmt.Errorf("trivy: base_url and username:password are required"))
	}
	// Connections have exactly one secret blob (task 2.2); Harbor needs
	// two values, so — matching the same "user:pass" convention task 2.2's
	// own Test Connection already established for Harbor — the secret is
	// "username:password".
	username, password, ok := strings.Cut(conn.Secret, ":")
	if !ok {
		return nil, collector.ErrConfig(fmt.Errorf("trivy: secret must be \"username:password\""))
	}
	return &session{client: harbor.New(conn.BaseURL, username, password, conn.Client)}, nil
}

type session struct{ client *harbor.Client }

func (s *session) Close() error { return nil }

func (s *session) Ping(ctx context.Context) error {
	return classify(s.client.Ping(ctx))
}

func (s *session) Discover(ctx context.Context, q collector.DiscoverQuery) ([]collector.Candidate, error) {
	projects, err := s.client.ListProjects(ctx)
	if err != nil {
		return nil, classify(err)
	}
	var out []collector.Candidate
	for _, p := range projects {
		repos, err := s.client.ListRepositories(ctx, p.Name)
		if err != nil {
			continue // one unreadable project shouldn't fail discovery for the rest
		}
		for _, r := range repos {
			if q.Search != "" && !strings.Contains(strings.ToLower(r.Name), strings.ToLower(q.Search)) {
				continue
			}
			out = append(out, collector.Candidate{ExternalKey: r.Name, Label: r.Name})
		}
	}
	return out, nil
}

func (s *session) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	project, repo, ok := splitExternalKey(bc.ExternalKey)
	if !ok {
		return collector.Resolved{OK: false, Summary: `external_key must be shaped "project/repo"`}, nil
	}
	art, err := s.client.LatestArtifactWithScan(ctx, project, repo)
	if err != nil {
		var se *harbor.StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
			return collector.Resolved{OK: false, Summary: fmt.Sprintf("no Harbor repository found at %q", bc.ExternalKey)}, nil
		}
		return collector.Resolved{}, classify(err)
	}
	if art == nil {
		return collector.Resolved{OK: false, Summary: fmt.Sprintf("repository %q has no artifacts yet", bc.ExternalKey)}, nil
	}
	return collector.Resolved{OK: true, Summary: fmt.Sprintf("found %q", bc.ExternalKey)}, nil
}

type bindingConfig struct {
	TagPattern    string `json:"tag_pattern"`
	SeverityFloor string `json:"severity_floor"`
}

func (s *session) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	project, repo, ok := splitExternalKey(req.ExternalKey)
	if !ok {
		return collector.Result{}, collector.ErrConfig(fmt.Errorf(`trivy: external_key must be shaped "project/repo", got %q`, req.ExternalKey))
	}
	var cfg bindingConfig
	_ = json.Unmarshal(req.Config, &cfg)

	art, err := s.client.FindArtifact(ctx, project, repo, cfg.TagPattern)
	if err != nil {
		return collector.Result{}, classify(err)
	}
	if art == nil {
		return collector.Result{}, collector.ErrNotFound(fmt.Errorf("trivy: no artifact found for %q (tag_pattern=%q)", req.ExternalKey, cfg.TagPattern))
	}

	summary := harbor.Summarize(art)
	var rawReport []byte
	if summary.HasReport {
		if vr, err := s.client.GetVulnerabilities(ctx, project, repo, art.Digest); err == nil && vr != nil {
			rawReport, _ = marshalVulnReport(vr)
		}
	}

	failOn := FailOnFromSettings(req.OrgSettings)
	return buildResult(trivyDetail{
		Source:         "registry",
		Digest:         art.Digest,
		Tags:           tagNames(art.Tags),
		Severity:       summary.Severity,
		Counts:         Counts{Total: summary.Total, Fixable: summary.Fixable, Critical: summary.Critical, High: summary.High, Medium: summary.Medium, Low: summary.Low, Unknown: summary.Unknown},
		NotScanned:     !summary.HasReport,
		ImagePushedAt:  timePtr(art.PushTime),
		ScanFinishedAt: summary.EndTime,
		RawReport:      rawReport,
	}, failOn)
}

func tagNames(tags []harbor.Tag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// splitExternalKey pulls the Harbor project out of "project/repo" —
// harbor.Client's own methods want the *full* external_key as their repo
// argument (they strip the "project/" prefix internally), so repo here is
// just externalKey itself, not the short name.
func splitExternalKey(externalKey string) (project, repo string, ok bool) {
	project, _, ok = strings.Cut(externalKey, "/")
	if !ok || project == "" {
		return "", "", false
	}
	return project, externalKey, true
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *harbor.StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden:
			return collector.ErrAuth(err)
		case se.StatusCode == http.StatusNotFound:
			return collector.ErrNotFound(err)
		case se.StatusCode == http.StatusTooManyRequests:
			return collector.ErrRateLimited(err, se.RetryAfter)
		case se.StatusCode >= 500:
			return collector.ErrUpstream(err)
		}
		return collector.ErrUpstream(err)
	}
	return collector.ErrUpstream(err)
}
