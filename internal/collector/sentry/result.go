package sentry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
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

	// Asked at most once, and only when an environment comes back empty.
	sawEvents, checked := req.PriorSignal, req.PriorSignal
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
		if len(issues) == 0 && !checked {
			checked = true
			var err error
			if sawEvents, err = s.hasReported(ctx, cfg, project.Slug); err != nil {
				warnings = append(warnings, collector.Warning{Step: "signal", Err: err.Error()})
			}
		}
		children = append(children, buildEnvironmentResult(env, cfg.ServiceTagValue, project, query, generatedAt, issues, sawEvents))
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

// hasReported looks for evidence the service has ever sent Sentry an event. In
// a shared project that is its value of the service tag; a dedicated project
// has no tag, but its environments only exist once events have arrived.
func (s *session) hasReported(ctx context.Context, cfg bindingConfig, projectSlug string) (bool, error) {
	if cfg.Mode == "dedicated" {
		envs, err := s.client.Environments(ctx, projectSlug)
		return len(envs) > 0, err
	}
	values, err := s.client.TagValues(ctx, projectSlug, cfg.ServiceTagKey)
	return slices.Contains(values, cfg.ServiceTagValue), err
}

// rollupHeadline is the binding-level summary task 3.6's dashboard
// projection reads generically (it never looks at Children — "generic
// code only reads Headline" is the rule this task exists to enforce).
// Picks the worst environment by Status (fail > warn > pass > unknown,
// task 3.6's own ordering), tying-break on the lower score, since that's
// the environment an operator would actually want surfaced first.
func rollupHeadline(children []collector.Result) collector.Headline {
	hs := make([]collector.Headline, 0, len(children))
	for _, c := range children {
		hs = append(hs, c.Headline)
	}
	return WorstHeadline(hs)
}

// WorstHeadline is rollupHeadline over headlines alone, for a caller that
// rebuilds them from stored snapshots (the reproject command). Since the
// status is read from the score, "worst" is the lowest score, and an
// environment with no signal only when none has one.
func WorstHeadline(children []collector.Headline) collector.Headline {
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
	worst := children[0]
	for _, h := range children[1:] {
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
func buildEnvironmentResult(env, serviceName string, project sentryclient.Project, query string, generatedAt time.Time, issues []sentryclient.Issue, sawEvents bool) collector.Result {
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
	status := scoring.EvalSentryStatus(true, true, sawEvents, signals, generatedAt)
	noSignal := status.Kind == scoring.SentryStatusNoSignal
	score := intPtr(status.Score)
	if noSignal {
		score = nil // nothing was measured, so nothing joins an average
	}

	facts := []collector.Fact{
		numFact("sentry.total_issues", float64(len(issues))),
		numFact("sentry.events_total", float64(totalEvents)),
		numFact("sentry.new", float64(newC)),
		numFact("sentry.escalating", float64(escalatingC)),
		numFact("sentry.regressed", float64(regressedC)),
		numFact("sentry.ongoing", float64(ongoingC)),
	}
	if !noSignal {
		facts = append(facts, numFact("sentry.score", float64(status.Score)))
	}

	headline := EnvironmentHeadline(env, score, status.Load, len(issues))
	if status.Kind != scoring.SentryStatusGraded {
		headline.Summary = fmt.Sprintf("%s: %s. %s", env, status.Label, status.Detail)
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
		Score: score, Grade: gradePtr, Load: float64Ptr(status.Load),
		Issues: issueDetails,
	})

	return collector.Result{
		Headline:   headline,
		Facts:      facts,
		Detail:     detail,
		DetailKind: string(collector.KindSentry),
	}
}

// EnvironmentHeadline is one environment's headline from its ported score
// (sentry.go, kept verbatim): score is already rounded to the nearest
// whole number, nil when Sentry has no signal from the service. The
// status is read from that number, not from the A–E grade the port also
// computes: a raw 79.6 shows as 80 and is healthy, where its grade (C,
// cut at an unrounded 80) used to paint an 80 red. The grade is still
// stored on the snapshot, but no longer carried on the headline or shown.
//
// Collect and the reproject command (which rebuilds it from a stored
// snapshot's score, load and issue count) both go through here.
func EnvironmentHeadline(env string, score *int, load float64, issues int) collector.Headline {
	ks := scoring.SentryScore(score, load, issues)
	h := collector.Headline{
		Status:     collector.StatusForScore(ks.Score),
		Score:      ks.Score,
		CountValue: int64Ptr(int64(issues)),
		CountLabel: "issue",
		Summary:    fmt.Sprintf("%s: %d unresolved issue(s), load %.1f", env, issues, load),
	}
	switch {
	case ks.Score == nil:
		h.StateLabel, h.CountValue = ks.Reason, nil
		h.Summary = env + ": no signal"
	case issues == 0:
		h.Summary = env + ": no unresolved issues"
	}
	return h
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
// environmentContentHash is what decides whether a sync changed anything:
// the environment and its issues. The score, grade and load are left out
// with the generation time — they decay as issues age, so hashing them made
// every sync "changed" and every sync a new snapshot.
func environmentContentHash(d environmentDetail) string {
	d.GeneratedAt, d.Score, d.Grade, d.Load = nil, nil, nil, nil
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
