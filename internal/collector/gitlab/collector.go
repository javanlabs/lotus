// Package gitlab implements task 3.1's Collector for GitLab, on top of
// internal/clients/gitlab's ported client. Registers itself in init(),
// per task 3.1's registry contract — importing this package for its side
// effect (see cmd/lotus's blank import) is the only wiring a caller needs.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/collector"
)

const (
	defaultInterval = 15 * time.Minute
	minInterval     = 5 * time.Minute
)

func init() {
	collector.Register(collector.KindGitLab, Collector{})
	collector.RegisterPersister(collector.KindGitLab, Persist)
}

type Collector struct{}

func (Collector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		Kind: collector.KindGitLab,
		ConnectionFields: []collector.Field{
			{Key: "base_url", Label: "GitLab URL", Type: "text", Required: true, Default: "https://gitlab.com"},
			{Key: "token", Label: "Personal/project access token", Type: "secret", Required: true},
		},
		BindingFields: []collector.Field{
			{Key: "default_branch", Label: "Default branch", Type: "text", Default: "main"},
			{Key: "stale_branch_days", Label: "Stale branch threshold (days)", Type: "number", Default: "30"},
			{Key: "pipeline_sample_size", Label: "Pipeline sample size", Type: "number", Default: "20"},
			{Key: "commit_window_days", Label: "Commit window (days)", Type: "number", Default: "30"},
		},
		BadgeStyle:      "gitlab",
		DefaultInterval: defaultInterval,
		MinInterval:     minInterval,
		CostHint:        6, // FetchHealth's own 6-call orchestration
		MetricKeys: []collector.MetricKeyDecl{
			{Key: "gitlab.pipeline_success_rate", Label: "Pipeline success rate", Unit: "%", ValueType: "number", Direction: "higher_better"},
			{Key: "gitlab.pipeline_sample_size", Label: "Pipeline sample size", Unit: "count", ValueType: "number", Direction: "neutral"},
			{Key: "gitlab.branches_total", Label: "Branches (total)", Unit: "count", ValueType: "number", Direction: "neutral"},
			{Key: "gitlab.branches_active", Label: "Branches (active)", Unit: "count", ValueType: "number", Direction: "neutral"},
			{Key: "gitlab.branches_stale", Label: "Branches (stale)", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "gitlab.commits_30d", Label: "Commits (30d)", Unit: "count", ValueType: "number", Direction: "higher_better"},
		},
	}
}

func (Collector) Open(ctx context.Context, conn collector.ConnConfig) (collector.Session, error) {
	if conn.BaseURL == "" || conn.Secret == "" {
		return nil, collector.ErrConfig(fmt.Errorf("gitlab: base_url and token are required"))
	}
	return &session{client: gitlab.New(conn.BaseURL, conn.Secret, conn.Client)}, nil
}

type session struct{ client *gitlab.Client }

func (s *session) Close() error { return nil }

func (s *session) Ping(ctx context.Context) error {
	return classify(s.client.Ping(ctx))
}

func (s *session) Discover(ctx context.Context, q collector.DiscoverQuery) ([]collector.Candidate, error) {
	projects, err := s.client.ListProjects(ctx)
	if err != nil {
		return nil, classify(err)
	}
	out := make([]collector.Candidate, 0, len(projects))
	for _, p := range projects {
		if q.Search != "" && !strings.Contains(strings.ToLower(p.PathWithNamespace), strings.ToLower(q.Search)) {
			continue
		}
		out = append(out, collector.Candidate{
			ExternalKey: p.PathWithNamespace, Label: p.Name,
			Metadata: map[string]any{"gitlab_project_id": p.ID},
		})
	}
	return out, nil
}

func (s *session) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	name, err := s.client.ProjectExists(ctx, bc.ExternalKey)
	if err != nil {
		var se *gitlab.StatusError
		if isStatusError(err, &se) && se.StatusCode == http.StatusNotFound {
			return collector.Resolved{OK: false, Summary: fmt.Sprintf("no GitLab project found at %q", bc.ExternalKey)}, nil
		}
		return collector.Resolved{}, classify(err)
	}
	return collector.Resolved{OK: true, Summary: fmt.Sprintf("found %q", name)}, nil
}

type bindingConfig struct {
	DefaultBranch      string `json:"default_branch"`
	StaleBranchDays    int    `json:"stale_branch_days"`
	PipelineSampleSize int    `json:"pipeline_sample_size"`
	CommitWindowDays   int    `json:"commit_window_days"`
}

func (s *session) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	var cfg bindingConfig
	_ = json.Unmarshal(req.Config, &cfg) // defaults already applied at binding-creation time (task 2.3)

	// All four binding fields reach the client now. Two of them —
	// pipeline_sample_size and commit_window_days — were offered in the
	// bind form from the start and quietly ignored, so a binding that set
	// them got the built-in 20 and 30 anyway. A control that does nothing
	// is worse than no control.
	h, err := s.client.FetchHealth(ctx, req.ExternalKey, gitlab.HealthOptions{
		DefaultBranch:      cfg.DefaultBranch,
		StaleDays:          cfg.StaleBranchDays,
		PipelineSampleSize: cfg.PipelineSampleSize,
		CommitWindowDays:   cfg.CommitWindowDays,
	})
	if err != nil {
		return collector.Result{}, classify(err)
	}
	return buildResult(h)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *gitlab.StatusError
	if isStatusError(err, &se) {
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
	return collector.ErrUpstream(err) // network error, timeout, etc.
}

func isStatusError(err error, target **gitlab.StatusError) bool {
	return errors.As(err, target)
}
