// Package sentry implements task 3.5's Collector: the most mature
// mapping model ported from legacy Lotus, with one performance trap
// (project-metadata memoization, see cache.go) that has to survive the
// port intact.
package sentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sentryclient "github.com/uyab/lotus/internal/clients/sentry"
	"github.com/uyab/lotus/internal/collector"
)

const (
	defaultInterval = 30 * time.Minute
	minInterval     = 5 * time.Minute

	// envAll is the environment label used when a binding names no
	// environments — task 3.5's "opt-in only" rule: Collect never calls
	// Client.Environments to auto-discover a project's full environment
	// list (that was the legacy default, and the reason 40 services × 6
	// environments meant 240 fetches per cycle for one organization). A
	// binding gets exactly this one unfiltered snapshot unless its own
	// config explicitly names environments.
	envAll = "all"

	// maxEnvironmentsPerBinding caps how many environments one binding's
	// config may name, so an operator opting in still can't accidentally
	// turn one Collect into dozens of Issues() calls.
	maxEnvironmentsPerBinding = 10

	// defaultIssueQuery mirrors the legacy config.go's own default
	// (SentryIssueQuery), applied here because task 2.3's ValidateConfig
	// leaves issue_query blank rather than defaulting it.
	defaultIssueQuery = "is:unresolved"
)

func init() {
	collector.Register(collector.KindSentry, Collector{})
	collector.RegisterPersister(collector.KindSentry, Persist)
}

type Collector struct{}

func (Collector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		Kind: collector.KindSentry,
		ConnectionFields: []collector.Field{
			{Key: "base_url", Label: "Sentry URL", Type: "text", Required: true, Default: "https://sentry.io"},
			{Key: "token", Label: "Auth token", Type: "secret", Required: true},
			// org is plain (non-secret) connection-level config, carried
			// via ConnConfig.Config rather than folded into Secret —
			// unlike Harbor's "username:password", this isn't a second
			// credential, just a second field Open needs.
			{Key: "org", Label: "Sentry organization slug", Type: "text", Required: true},
		},
		// Mirrors internal/integrations/descriptor_sentry.go's own
		// ConfigFields exactly (task 2.3 already shipped binding-config
		// validation for Sentry) — this copy exists only so Descriptor()
		// documents the same shape task 2.3 already enforces; it isn't a
		// second source of truth ValidateConfig defers to.
		BindingFields: []collector.Field{
			{Key: "mode", Label: "Mode", Type: "select", Options: []string{"shared", "dedicated"}, Required: true},
			{Key: "service_tag_key", Label: "Service tag key", Type: "text", Default: "service", RequiredIf: map[string]string{"mode": "shared"}},
			{Key: "service_tag_value", Label: "Service tag value", Type: "text", RequiredIf: map[string]string{"mode": "shared"}},
			{Key: "environments", Label: "Environments (blank = one unfiltered pull)", Type: "multiselect"},
			{Key: "issue_query", Label: "Issue query", Type: "text", Default: defaultIssueQuery},
			{Key: "stats_period", Label: "Stats period", Type: "text", Default: "14d"},
			{Key: "max_issues", Label: "Max issues", Type: "number", Default: "100"},
		},
		BadgeStyle:      "sentry",
		DefaultInterval: defaultInterval,
		MinInterval:     minInterval,
		CostHint:        2, // 1 (cached) project fetch + 1 issues call per named environment
		MetricKeys: []collector.MetricKeyDecl{
			{Key: "sentry.score", Label: "Health score", Unit: "score", ValueType: "number", Direction: "higher_better"},
			{Key: "sentry.total_issues", Label: "Unresolved issues", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sentry.events_total", Label: "Events", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sentry.new", Label: "New", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sentry.escalating", Label: "Escalating", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sentry.regressed", Label: "Regressed", Unit: "count", ValueType: "number", Direction: "lower_better"},
			{Key: "sentry.ongoing", Label: "Ongoing", Unit: "count", ValueType: "number", Direction: "lower_better"},
		},
	}
}

type connConfig struct {
	Org string `json:"org"`
}

func (Collector) Open(ctx context.Context, conn collector.ConnConfig) (collector.Session, error) {
	if conn.BaseURL == "" || conn.Secret == "" {
		return nil, collector.ErrConfig(fmt.Errorf("sentry: base_url and token are required"))
	}
	var cc connConfig
	if len(conn.Config) > 0 {
		if err := json.Unmarshal(conn.Config, &cc); err != nil {
			return nil, collector.ErrConfig(fmt.Errorf("sentry: invalid connection config: %w", err))
		}
	}
	if cc.Org == "" {
		return nil, collector.ErrConfig(fmt.Errorf("sentry: connection config must set \"org\""))
	}
	return &session{
		client:  sentryclient.New(conn.BaseURL, conn.Secret, cc.Org, conn.Client),
		baseURL: conn.BaseURL, org: cc.Org, secret: conn.Secret,
	}, nil
}

type session struct {
	client *sentryclient.Client
	// baseURL/org/secret are kept alongside client purely to key the
	// shared project cache (cache.go) — the client itself has no way to
	// hand these back out once constructed.
	baseURL, org, secret string
}

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
		if q.Search != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.Slug), strings.ToLower(q.Search)) {
			continue
		}
		out = append(out, collector.Candidate{ExternalKey: p.Slug, Label: p.Name})
	}
	return out, nil
}

func (s *session) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	_, err := s.client.Project(ctx, bc.ExternalKey)
	if err != nil {
		var se *sentryclient.StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
			return collector.Resolved{OK: false, Summary: fmt.Sprintf("no Sentry project found at %q", bc.ExternalKey)}, nil
		}
		return collector.Resolved{}, classify(err)
	}
	return collector.Resolved{OK: true, Summary: fmt.Sprintf("found %q", bc.ExternalKey)}, nil
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sentryclient.StatusError
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
