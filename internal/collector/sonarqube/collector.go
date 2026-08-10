// Package sonarqube implements task 3.1's Collector for SonarQube, on top
// of internal/clients/sonar's ported client. Registers itself in init(),
// same contract as internal/collector/gitlab.
package sonarqube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
)

const (
	defaultInterval = 30 * time.Minute // SonarQube analyses run far less often than every commit
	minInterval     = 10 * time.Minute
)

func init() {
	collector.Register(collector.KindSonarQube, Collector{})
	collector.RegisterPersister(collector.KindSonarQube, Persist)
}

type Collector struct{}

func (Collector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		Kind: collector.KindSonarQube,
		ConnectionFields: []collector.Field{
			{Key: "base_url", Label: "SonarQube URL", Type: "text", Required: true},
			{Key: "token", Label: "User token", Type: "secret", Required: true},
		},
		BindingFields: []collector.Field{
			{Key: "branch", Label: "Branch", Type: "text"},
			{Key: "exclusions", Label: "Path exclusions", Type: "multiselect"},
		},
		BadgeStyle:      "sonarqube",
		DefaultInterval: defaultInterval,
		MinInterval:     minInterval,
		CostHint:        3, // Fetch's own 3-call orchestration
		MetricKeys: []collector.MetricKeyDecl{
			{Key: "sonar.coverage", Label: "Coverage", Unit: "%", ValueType: "number", Direction: "higher_better"},
			{Key: "sonar.bugs", Label: "Bugs", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.vulnerabilities", Label: "Vulnerabilities", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.code_smells", Label: "Code smells", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.security_hotspots", Label: "Security hotspots", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.duplicated_lines_density", Label: "Duplicated lines", Unit: "%", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.ncloc", Label: "Lines of code", Unit: "count", ValueType: "number", Direction: "neutral"},
			{Key: "sonar.complexity", Label: "Complexity", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.sqale_index_min", Label: "Technical debt", Unit: "minutes", ValueType: "number", Direction: "lower_better"},
			{Key: "sonar.test_execution_time_ms", Label: "Test execution time", Unit: "ms", ValueType: "number", Direction: "neutral"},
			{Key: "sonar.reliability_rating", Label: "Reliability rating", Unit: "grade", ValueType: "text", Direction: "lower_better"},
			{Key: "sonar.security_rating", Label: "Security rating", Unit: "grade", ValueType: "text", Direction: "lower_better"},
			{Key: "sonar.sqale_rating", Label: "Maintainability rating", Unit: "grade", ValueType: "text", Direction: "lower_better"},
		},
	}
}

func (Collector) Open(ctx context.Context, conn collector.ConnConfig) (collector.Session, error) {
	if conn.BaseURL == "" || conn.Secret == "" {
		return nil, collector.ErrConfig(fmt.Errorf("sonarqube: base_url and token are required"))
	}
	return &session{client: sonar.New(conn.BaseURL, conn.Secret, conn.Client)}, nil
}

type session struct{ client *sonar.Client }

func (s *session) Close() error { return nil }

func (s *session) Ping(ctx context.Context) error {
	return classify(s.client.Ping(ctx))
}

func (s *session) Discover(ctx context.Context, q collector.DiscoverQuery) ([]collector.Candidate, error) {
	projects, err := s.client.ListProjects(ctx)
	if err != nil {
		var se *sonar.StatusError
		if isStatusError(err, &se) && se.StatusCode == http.StatusForbidden {
			// task 2.4's documented degrade: a token without admin scope
			// can't list, but isn't broken — Discover returns an empty,
			// non-error result rather than making the caller special-case
			// this the way the old ad hoc discoverSonarQube (task 2.4) had
			// to with its own DiscoverDegradedNoAdminScope sentinel.
			return nil, nil
		}
		return nil, classify(err)
	}
	out := make([]collector.Candidate, 0, len(projects))
	for _, p := range projects {
		if q.Search != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(q.Search)) && !strings.Contains(strings.ToLower(p.Key), strings.ToLower(q.Search)) {
			continue
		}
		out = append(out, collector.Candidate{ExternalKey: p.Key, Label: p.Name})
	}
	return out, nil
}

func (s *session) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	name, err := s.client.ProjectExists(ctx, bc.ExternalKey)
	if err != nil {
		var se *sonar.StatusError
		if isStatusError(err, &se) && se.StatusCode == http.StatusNotFound {
			return collector.Resolved{OK: false, Summary: fmt.Sprintf("no SonarQube project found with key %q", bc.ExternalKey)}, nil
		}
		return collector.Resolved{}, classify(err)
	}
	return collector.Resolved{OK: true, Summary: fmt.Sprintf("found %q", name)}, nil
}

type bindingConfig struct {
	Branch string `json:"branch"`
}

func (s *session) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	var cfg bindingConfig
	_ = json.Unmarshal(req.Config, &cfg)

	m, err := s.client.Fetch(ctx, req.ExternalKey, cfg.Branch)
	if err != nil {
		return collector.Result{}, classify(err)
	}
	return buildResult(m)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sonar.StatusError
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
	return collector.ErrUpstream(err)
}

// isStatusError uses errors.As, not a bare type assertion: the sonar
// client's Fetch wraps the measures call's error with fmt.Errorf("measures:
// %w", err) (task 3.3's port keeps that wrapping as-is), so the
// *StatusError this needs to find is often one level deeper than err
// itself.
func isStatusError(err error, target **sonar.StatusError) bool {
	return errors.As(err, target)
}
