package sentry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	sentryclient "github.com/uyab/lotus/internal/clients/sentry"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
	"github.com/uyab/lotus/internal/tenant"
)

// bindingConfig mirrors internal/integrations/descriptor_sentry.go's own
// (unexported) sentryConfig field-for-field — task 2.3 already validates
// and defaults a binding's config against that shape before it's ever
// stored, so this is just this package's own JSON view onto the same
// data, not a second source of truth.
type bindingConfig struct {
	Mode            string   `json:"mode"`
	ServiceTagKey   string   `json:"service_tag_key"`
	ServiceTagValue string   `json:"service_tag_value"`
	Environments    []string `json:"environments"`
	IssueQuery      string   `json:"issue_query"`
	StatsPeriod     string   `json:"stats_period"`
	MaxIssues       int      `json:"max_issues"`
}

func (s *session) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	var cfg bindingConfig
	if err := json.Unmarshal(req.Config, &cfg); err != nil {
		return collector.Result{}, collector.ErrConfig(fmt.Errorf("sentry: invalid binding config: %w", err))
	}
	if cfg.Mode != "shared" && cfg.Mode != "dedicated" {
		return collector.Result{}, collector.ErrConfig(fmt.Errorf(`sentry: binding config "mode" must be "shared" or "dedicated", got %q`, cfg.Mode))
	}

	project, err := sharedProjectCache.get(ctx, s.client, s.baseURL, s.org, s.secret, req.ExternalKey)
	if err != nil {
		return collector.Result{}, classify(err)
	}

	query := cfg.IssueQuery
	if query == "" {
		query = defaultIssueQuery
	}
	// In dedicated mode the whole project already IS this one binding, so
	// the query is sent unfiltered — a tag filter here would match
	// nothing, since these projects have no reason to tag at all.
	if cfg.Mode == "shared" {
		query = sentryclient.TagFilterQuery(query, cfg.ServiceTagKey, cfg.ServiceTagValue)
	}

	envs := cfg.Environments
	var warnings []collector.Warning
	if len(envs) > maxEnvironmentsPerBinding {
		warnings = append(warnings, collector.Warning{
			Step: "environments",
			Err:  fmt.Sprintf("binding names %d environments, capped at %d", len(envs), maxEnvironmentsPerBinding),
		})
		envs = envs[:maxEnvironmentsPerBinding]
	}
	if len(envs) == 0 {
		envs = []string{envAll}
	}

	generatedAt := time.Now()
	children := make([]collector.Result, 0, len(envs))
	for _, env := range envs {
		issueEnv := env
		if issueEnv == envAll {
			issueEnv = ""
		}
		issues, err := s.client.Issues(ctx, sentryclient.IssueQuery{
			ProjectSlug: project.Slug, Environment: issueEnv, Query: query,
			StatsPeriod: cfg.StatsPeriod, Limit: cfg.MaxIssues,
		})
		if err != nil {
			// One bad environment shouldn't fail every other environment
			// this binding also asked for — task 3.1's partial-failure
			// rule: keep what worked, warn about what didn't.
			warnings = append(warnings, collector.Warning{Step: "issues:" + env, Err: err.Error()})
			continue
		}
		children = append(children, buildEnvironmentResult(env, cfg.ServiceTagValue, project, query, generatedAt, issues))
	}

	if len(children) == 0 {
		return collector.Result{}, collector.ErrUpstream(fmt.Errorf("sentry: every configured environment failed"))
	}

	return collector.Result{
		Headline:   rollupHeadline(children),
		Children:   children,
		Warnings:   warnings,
		DetailKind: string(collector.KindSentry),
	}, nil
}

// rollupHeadline is the binding-level summary task 3.6's dashboard
// projection reads generically (it never looks at Children — "generic
// code only reads Headline" is the rule this task exists to enforce).
// Picks the worst environment by Status (fail > warn > pass > unknown,
// task 3.6's own ordering), tying-break on the lower score, since that's
// the environment an operator would actually want surfaced first.
func rollupHeadline(children []collector.Result) collector.Headline {
	severity := func(s collector.Status) int {
		switch s {
		case collector.StatusFail:
			return 3
		case collector.StatusPass:
			return 1
		default:
			return 0
		}
	}
	worst := children[0].Headline
	for _, c := range children[1:] {
		h := c.Headline
		switch {
		case severity(h.Status) > severity(worst.Status):
			worst = h
		case severity(h.Status) == severity(worst.Status) && h.Score != nil && worst.Score != nil && *h.Score < *worst.Score:
			worst = h
		}
	}
	if len(children) > 1 {
		worst.Summary = fmt.Sprintf("%s (worst of %d environments)", worst.Summary, len(children))
	}
	return worst
}

// environmentDetail is what gets marshaled into each child Result's
// Detail and, on the persistence path, unmarshaled straight into a
// sentry_snapshots row (+ its sentry_issues rows).
type environmentDetail struct {
	Environment     string
	ServiceName     string
	SentryProjectID int64
	ProjectSlug     string
	ProjectName     string
	Platform        string
	SentryOrg       string
	TeamID          int64
	TeamSlug        string
	StatusFilter    string
	GeneratedAt     *time.Time
	TotalIssues     int
	Score           *int
	Grade           *string
	Load            *float64
	Issues          []issueDetail
}

type issueDetail struct {
	IssueID    string
	ShortID    string
	Title      string
	Status     string
	Substatus  string
	EventCount int
	FirstSeen  *time.Time
	LastSeen   *time.Time
	Permalink  string
}

// buildEnvironmentResult turns one environment's pulled issues into a
// leaf Result: Facts for the dashboard/timeseries (task 3.7), Headline
// for the fleet view, Detail for Persist. mapped/hasSnapshot are always
// true here — Collect only ever runs for a binding that exists and just
// pulled live, so evalSentryStatus's "not configured yet" / "never
// synced" branches are unreachable from this call site by construction;
// they exist for task 3.6's future dashboard reads of stale/absent state.
func buildEnvironmentResult(env, serviceName string, project sentryclient.Project, query string, generatedAt time.Time, issues []sentryclient.Issue) collector.Result {
	signals := make([]scoring.SentryIssueSignal, 0, len(issues))
	totalEvents, newC, escalatingC, regressedC, ongoingC := 0, 0, 0, 0, 0
	for _, is := range issues {
		signals = append(signals, scoring.SentryIssueSignal{
			Substatus: is.Substatus, EventCount: is.Count, LastSeen: nullTimeFrom(is.LastSeen),
		})
		totalEvents += is.Count
		switch is.Substatus {
		case "new":
			newC++
		case "escalating":
			escalatingC++
		case "regressed":
			regressedC++
		case "ongoing":
			ongoingC++
		}
	}
	status := scoring.EvalSentryStatus(true, true, 1, signals, generatedAt)

	facts := []collector.Fact{
		numFact("sentry.score", float64(status.Score)),
		numFact("sentry.total_issues", float64(len(issues))),
		numFact("sentry.events_total", float64(totalEvents)),
		numFact("sentry.new", float64(newC)),
		numFact("sentry.escalating", float64(escalatingC)),
		numFact("sentry.regressed", float64(regressedC)),
		numFact("sentry.ongoing", float64(ongoingC)),
	}

	headline := collector.Headline{
		Grade:      status.Grade,
		Score:      intPtr(status.Score),
		CountValue: int64Ptr(int64(len(issues))),
		CountLabel: "issue",
		Summary:    fmt.Sprintf("%s: %d unresolved issue(s), load %.1f", env, len(issues), status.Load),
	}
	switch {
	case status.Failed:
		headline.Status = collector.StatusFail
	case status.Grade == "A" || status.Grade == "B":
		headline.Status = collector.StatusPass
	case status.Grade == "C":
		headline.Status = collector.StatusFail
	default: // D, E
		headline.Status = collector.StatusFail
	}

	issueDetails := make([]issueDetail, 0, len(issues))
	for _, is := range issues {
		issueDetails = append(issueDetails, issueDetail{
			IssueID: is.ID, ShortID: is.ShortID, Title: is.Title, Status: is.Status, Substatus: is.Substatus,
			EventCount: is.Count, FirstSeen: is.FirstSeen, LastSeen: is.LastSeen, Permalink: is.Permalink,
		})
	}
	var gradePtr *string
	if status.Grade != "" {
		gradePtr = &status.Grade
	}
	detail, _ := json.Marshal(environmentDetail{
		Environment: env, ServiceName: serviceName,
		SentryProjectID: project.ID, ProjectSlug: project.Slug, ProjectName: project.Name,
		Platform: project.Platform, SentryOrg: project.Org, TeamID: project.TeamID, TeamSlug: project.TeamSlug,
		StatusFilter: query, GeneratedAt: &generatedAt, TotalIssues: len(issues),
		Score: intPtr(status.Score), Grade: gradePtr, Load: float64Ptr(status.Load),
		Issues: issueDetails,
	})

	return collector.Result{
		Headline:   headline,
		Facts:      facts,
		Detail:     detail,
		DetailKind: string(collector.KindSentry),
	}
}

func numFact(key string, v float64) collector.Fact {
	vv := v
	return collector.Fact{Key: key, ValueNum: &vv, Unit: "count"}
}

func intPtr(v int) *int             { return &v }
func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func nullTimeFrom(t *time.Time) sql.NullTime {
	if t == nil || t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// environmentContentHash hashes d with GeneratedAt zeroed out — task
// 3.6's dedupe rule needs "did the upstream data change", and GeneratedAt
// is stamped from time.Now() fresh on every single Collect regardless of
// whether the issue list changed at all, so including it would make every
// collect look different and defeat dedupe entirely for this kind (the
// other three kinds don't have this problem: none of them stamp a
// collect-time timestamp into their own Detail).
func environmentContentHash(d environmentDetail) string {
	d.GeneratedAt = nil
	b, _ := json.Marshal(d)
	return collector.ContentHash(collector.Result{Detail: b})
}

// Persist writes a Collect's per-environment Children into
// sentry_snapshots (+ sentry_issues), replacing every environment
// currently stored for this binding in one transaction.
func Persist(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, r collector.Result) error {
	snaps := make([]repo.NewSentrySnapshot, 0, len(r.Children))
	for _, child := range r.Children {
		var d environmentDetail
		if err := json.Unmarshal(child.Detail, &d); err != nil {
			return err
		}
		issues := make([]repo.NewSentryIssue, 0, len(d.Issues))
		for _, is := range d.Issues {
			issues = append(issues, repo.NewSentryIssue{
				IssueID: is.IssueID, ShortID: is.ShortID, Title: is.Title, Status: is.Status, Substatus: is.Substatus,
				EventCount: is.EventCount, FirstSeen: is.FirstSeen, LastSeen: is.LastSeen, Permalink: is.Permalink,
			})
		}
		snaps = append(snaps, repo.NewSentrySnapshot{
			ComponentID: componentID, ContentHash: environmentContentHash(d),
			Environment: d.Environment, ServiceName: d.ServiceName,
			SentryProjectID: d.SentryProjectID, ProjectSlug: d.ProjectSlug, ProjectName: d.ProjectName,
			Platform: d.Platform, SentryOrg: d.SentryOrg, TeamID: d.TeamID, TeamSlug: d.TeamSlug,
			StatusFilter: d.StatusFilter, GeneratedAt: d.GeneratedAt, TotalIssues: d.TotalIssues,
			Score: d.Score, Grade: d.Grade, Load: d.Load, Issues: issues,
		})
	}
	return repo.ReplaceSentrySnapshots(ctx, s, componentID, bindingID, r.Headline, snaps)
}
