package web

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// The dashboard (/dashboard) is where a member lands after signing in: the
// numbers that matter across the organization, what needs the team's
// attention, how active each project's QA has been this month, and the
// latest issue activity. The handler loads; buildHomeDashboard, which does
// no I/O, decides what is shown, so the decisions are tested without a
// database.

const (
	// homeAlertCap is how many alerts the page lists before "N more".
	homeAlertCap = 10
	// homeTopN is how many rows each project-activity list ranks.
	homeTopN = 5
	// homeActivityN is how many issue happenings the feed shows.
	homeActivityN = 10
	// failingBindingAfter is how many failed syncs in a row make a
	// binding an alert: one failure is a blip, three is a pattern.
	failingBindingAfter = 3
)

// severeIssueSeverities are the issue severities that raise an alert
// while the issue is open.
var severeIssueSeverities = []string{"blocker", "critical"}

// homeInputs is everything the dashboard is built from. QA fields are
// zero when the principal may not read QA (CanQA false).
type homeInputs struct {
	Now             time.Time
	OrgName         string
	ProjectCount    int
	Components      []repo.LiveComponent
	Metrics         map[uuid.UUID]repo.ComponentMetric
	FailedSyncs     int // in the last 24 hours
	Connections     []repo.Connection
	FailingBindings []repo.FailingBinding

	CanQA, CanWorkload bool
	OpenIssues         int
	OpenPlans          int
	AssignedToMe       int
	SevereIssues       []repo.QAIssueSummary
	Workload           *repo.QAWorkload
	Activity           []repo.QAIssueActivity
}

// homeFigure is one number in the summary; Href, when set, is where it is
// explained.
type homeFigure struct {
	Label, Value, Tone, Href string
}

// homeAlert is one thing that needs the team's attention. Tone is fail or
// warn; at orders alerts of one tone, newest first.
type homeAlert struct {
	Tone, Title, Detail, Href, When string
	at                              time.Time
}

// homeTop is one ranked list of the project-activity panel.
type homeTop struct {
	Title, Empty string
	Rows         []homeTopRow
}

type homeTopRow struct {
	Name, Href, Note string
	Value            int
}

// homeEvent is one line of the recent-activity feed.
type homeEvent struct {
	Actor, Verb, Issue, Href, Project, When, Tone string
}

type homeDashboard struct {
	Subtitle   string
	Figures    []homeFigure
	Rates      []kpiCard
	Alerts     []homeAlert
	MoreAlerts int
	ShowQA     bool
	// Tops are the project-activity lists for TopsMonth ("October 2026").
	Tops      []homeTop
	TopsMonth string
	Activity  []homeEvent
}

func buildHomeDashboard(in homeInputs) homeDashboard {
	d := homeDashboard{ShowQA: in.CanQA}
	d.Subtitle = fmt.Sprintf("%d project%s · %d component%s across %s",
		in.ProjectCount, plural(in.ProjectCount, "", "s"), len(in.Components), plural(len(in.Components), "", "s"), in.OrgName)

	failing, stale := 0, 0
	var alerts []homeAlert
	for _, c := range in.Components {
		cm, ok := in.Metrics[c.ID]
		if !ok {
			continue
		}
		project := &repo.Project{ID: c.ProjectID, Slug: c.ProjectSlug}
		comp := &repo.Component{ID: c.ID, Slug: c.Slug}
		kinds := cm.Metrics()
		if cm.StatusValue() == collector.StatusFail {
			failing++
			var labels []string
			first := ""
			for _, k := range bindableKinds {
				if kh, ok := kinds[k]; ok && collector.Status(kh.Status) == collector.StatusFail {
					labels = append(labels, kindLabels[k])
					if first == "" {
						first = k
					}
				}
			}
			href := componentURL(project, comp)
			if first != "" {
				href = metricURL(project, comp, first)
			}
			detail := c.ProjectName
			if len(labels) > 0 {
				detail += " · " + strings.Join(labels, ", ")
			}
			alerts = append(alerts, homeAlert{Tone: "fail", Title: c.Name + " is failing", Detail: detail, Href: href,
				When: relativeTimeStrAt(cm.UpdatedAt, in.Now), at: cm.UpdatedAt})
		}
		for _, k := range bindableKinds {
			kh, ok := kinds[k]
			if !ok {
				continue
			}
			at := kindSyncedAt(cm, kh)
			if staleNote(k, at, in.Now) == "" {
				continue
			}
			stale++
			alerts = append(alerts, homeAlert{Tone: "warn", Title: kindLabels[k] + " on " + c.Name + " has not synced in over a day",
				Detail: c.ProjectName + " · last synced " + relativeTimeStrAt(at, in.Now), Href: metricURL(project, comp, k),
				When: relativeTimeStrAt(at, in.Now), at: at})
		}
	}
	for _, cn := range in.Connections {
		if cn.Status != "auth_failed" && cn.Status != "error" {
			continue
		}
		title := "Connection " + cn.Name + " cannot sign in"
		if cn.Status == "error" {
			title = "Connection " + cn.Name + " is failing"
		}
		a := homeAlert{Tone: "fail", Title: title, Detail: integrationProviderLabel(cn.Provider), Href: "/settings/connections"}
		if cn.LastError != nil && *cn.LastError != "" {
			a.Detail += " · " + *cn.LastError
		}
		if cn.LastVerifiedAt != nil {
			a.at, a.When = *cn.LastVerifiedAt, relativeTimeStrAt(*cn.LastVerifiedAt, in.Now)
		}
		alerts = append(alerts, a)
	}
	for _, b := range in.FailingBindings {
		project := &repo.Project{ID: b.ProjectID, Slug: b.ProjectSlug}
		comp := &repo.Component{ID: b.ID, Slug: b.Slug}
		alerts = append(alerts, homeAlert{Tone: "fail", Title: kindLabels[b.Kind] + " sync keeps failing on " + b.Name,
			Detail: fmt.Sprintf("%s · %d failed syncs in a row", b.ProjectName, b.ConsecutiveFailures),
			Href:   metricURL(project, comp, b.Kind) + "/runs"})
	}
	if in.CanQA {
		for _, is := range in.SevereIssues {
			alerts = append(alerts, homeAlert{Tone: "fail",
				Title:  fmt.Sprintf("%s issue #%d: %s", capitalize(is.Severity), is.Key, is.Title),
				Detail: is.ProjectName + " · " + is.Status, Href: issueURLFor(is),
				When: relativeTimeStrAt(is.CreatedAt, in.Now), at: is.CreatedAt})
		}
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		if alerts[i].Tone != alerts[j].Tone {
			return alerts[i].Tone == "fail"
		}
		return alerts[i].at.After(alerts[j].at)
	})
	if len(alerts) > homeAlertCap {
		d.MoreAlerts = len(alerts) - homeAlertCap
		alerts = alerts[:homeAlertCap]
	}
	d.Alerts = alerts

	attention := func(n int) string { return ternary(n > 0, "fail", "pass") }
	d.Figures = []homeFigure{
		{Label: "PROJECTS", Value: thousands(int64(in.ProjectCount)), Tone: "neutral", Href: "/projects"},
		{Label: "COMPONENTS", Value: thousands(int64(len(in.Components))), Tone: "neutral", Href: "/projects"},
		{Label: "FAILING COMPONENTS", Value: thousands(int64(failing)), Tone: attention(failing), Href: "/projects"},
		{Label: "STALE METRICS", Value: thousands(int64(stale)), Tone: attention(stale)},
		{Label: "SYNC FAILURES · 24H", Value: thousands(int64(in.FailedSyncs)), Tone: attention(in.FailedSyncs), Href: "/activity"},
	}
	if in.CanQA {
		d.Figures = append(d.Figures,
			homeFigure{Label: "OPEN ISSUES", Value: thousands(int64(in.OpenIssues)), Tone: "neutral", Href: "/qa"},
			homeFigure{Label: "OPEN PLANS", Value: thousands(int64(in.OpenPlans)), Tone: "neutral", Href: "/qa"},
			homeFigure{Label: "ASSIGNED TO ME", Value: thousands(int64(in.AssignedToMe)), Tone: "neutral", Href: "/qa/my"},
		)
	}

	infos := make([]componentInfo, 0, len(in.Components))
	for _, c := range in.Components {
		infos = append(infos, componentInfo{ID: c.ID, Name: c.Name, Slug: c.Slug, ProjectID: c.ProjectID, ProjectName: c.ProjectName, ProjectSlug: c.ProjectSlug})
	}
	d.Rates = projectKPIs(infos, in.Metrics)

	if in.CanQA && in.Workload != nil {
		d.TopsMonth = monthLabel(in.Workload.Month)
		d.Tops = homeTops(in.Workload, in.CanWorkload)
	}
	if in.CanQA {
		for _, a := range in.Activity {
			d.Activity = append(d.Activity, homeEventFor(a, in.Now))
		}
	}
	return d
}

// homeTops ranks the month's QA activity: projects by tests executed,
// projects by issues reported and, for whoever may read the workload,
// testers by tests executed. A row with nothing to count is left out; ties
// go by name.
func homeTops(w *repo.QAWorkload, canWorkload bool) []homeTop {
	type row struct {
		homeTopRow
		key string
	}
	rank := func(rows []row) []homeTopRow {
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Value != rows[j].Value {
				return rows[i].Value > rows[j].Value
			}
			return rows[i].key < rows[j].key
		})
		out := []homeTopRow{}
		for _, r := range rows {
			if r.Value > 0 && len(out) < homeTopN {
				out = append(out, r.homeTopRow)
			}
		}
		return out
	}

	var tested, issues []row
	for _, p := range w.Projects {
		base := qaBaseFor(p.ProjectID, p.ProjectSlug)
		key := strings.ToLower(p.Name)
		tested = append(tested, row{homeTopRow{Name: p.Name, Href: base, Value: p.Total - p.Untested,
			Note: fmt.Sprintf("%d case%s written", p.CasesCreated, plural(p.CasesCreated, "", "s"))}, key})
		issues = append(issues, row{homeTopRow{Name: p.Name, Href: base + "/issues", Value: p.IssuesCreated}, key})
	}
	tops := []homeTop{
		{Title: "TESTS EXECUTED", Empty: "No tests executed yet this month.", Rows: rank(tested)},
		{Title: "ISSUES REPORTED", Empty: "No issues reported yet this month.", Rows: rank(issues)},
	}
	if canWorkload {
		var testers []row
		for _, p := range w.People {
			testers = append(testers, row{homeTopRow{Name: p.Name, Href: "/qa/workload", Value: p.Total - p.Untested,
				Note: fmt.Sprintf("%d issue%s reported", p.IssuesReported, plural(p.IssuesReported, "", "s"))}, strings.ToLower(p.Name)})
		}
		tops = append(tops, homeTop{Title: "TOP TESTERS", Empty: "Nobody has executed a test yet this month.", Rows: rank(testers)})
	}
	return tops
}

// homeEventFor words one issue happening for the feed.
func homeEventFor(a repo.QAIssueActivity, now time.Time) homeEvent {
	e := homeEvent{Actor: a.Actor, Issue: fmt.Sprintf("#%d %s", a.Key, a.Title), Href: issueURLFor(a.QAIssueSummary),
		Project: a.ProjectName, When: relativeTimeStrAt(a.At, now), Tone: "none"}
	if e.Actor == "" {
		e.Actor = "Someone"
	}
	switch a.Kind {
	case "reported":
		e.Verb = "reported"
		if slices.Contains(severeIssueSeverities, a.Severity) {
			e.Tone = "fail"
		}
	case "status":
		to := ""
		if a.StatusTo != nil {
			to = *a.StatusTo
		}
		e.Verb = "marked " + to
		switch to {
		case "fixed", "closed":
			e.Tone = "pass"
		case "reopened":
			e.Tone = "fail"
		}
	default:
		e.Verb = "commented on"
	}
	return e
}

// issueURLFor is an issue's page from a cross-project summary row.
func issueURLFor(is repo.QAIssueSummary) string {
	return fmt.Sprintf("%s/issues/%d", qaBaseFor(is.ProjectID, is.ProjectSlug), is.Key)
}

// monthLabel writes "2026-10" as "October 2026"; anything else as given.
func monthLabel(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.Format("January 2006")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ternary(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// integrationProviderLabel names a connection's provider as the kind it
// serves is named everywhere else.
func integrationProviderLabel(provider string) string {
	if provider == "harbor_trivy" {
		return connectionLabels["trivy"]
	}
	if l, ok := kindLabels[provider]; ok {
		return l
	}
	return provider
}

// homeDashboardPage is GET /dashboard.
func homeDashboardPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		p := mustPrincipal(c)
		ctx := c.Context()
		now := dashboardNow()
		// QA panels need both the module switched on and the permission; with
		// LOTUS_FEATURE_QA off they would link into routes that do not exist.
		qaOn := d.Cfg.Features.QA
		in := homeInputs{Now: now, OrgName: org.Name,
			CanQA: qaOn && p.Can("qa.read"), CanWorkload: qaOn && p.Can("qa.workload.read")}

		projects, err := repo.ListProjects(ctx, s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		in.ProjectCount = len(projects)
		if in.Components, err = repo.ListLiveComponents(ctx, s); err != nil {
			return writeRepoErr(c, err)
		}
		metricRows, err := repo.ListComponentMetricsForOrg(ctx, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		in.Metrics = make(map[uuid.UUID]repo.ComponentMetric, len(metricRows))
		for _, m := range metricRows {
			in.Metrics[m.ComponentID] = m
		}
		if in.FailedSyncs, err = repo.CountFailedSyncsSince(ctx, s, now.Add(-24*time.Hour)); err != nil {
			return writeRepoErr(c, err)
		}
		if in.Connections, err = repo.ListConnections(ctx, s); err != nil {
			return writeRepoErr(c, err)
		}
		if in.FailingBindings, err = repo.ListFailingBindings(ctx, s, failingBindingAfter); err != nil {
			return writeRepoErr(c, err)
		}

		if in.CanQA {
			summaries, err := repo.ListQAProjectSummaries(ctx, s, nil)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, r := range summaries {
				in.OpenIssues += r.OpenIssues
				in.OpenPlans += r.OpenPlans
			}
			if in.AssignedToMe, err = repo.CountMyQAItems(ctx, s, p.UserID, nil); err != nil {
				return writeRepoErr(c, err)
			}
			if in.SevereIssues, err = repo.ListOpenQAIssuesBySeverity(ctx, s, severeIssueSeverities); err != nil {
				return writeRepoErr(c, err)
			}
			month, err := repo.CurrentQAMonth(ctx, s)
			if err != nil {
				return writeRepoErr(c, err)
			}
			if in.Workload, err = repo.LoadQAWorkload(ctx, s, month); err != nil {
				return writeRepoErr(c, err)
			}
			if in.Activity, err = repo.ListRecentQAIssueActivity(ctx, s, homeActivityN); err != nil {
				return writeRepoErr(c, err)
			}
		}

		c.Type("html")
		return renderPage(c, d, "pages/dashboard.html", httpx.ViewData{
			Title: "Dashboard", Org: org, User: user, Nav: buildNav("dashboard", len(projects)),
			Data: buildHomeDashboard(in),
		})
	}
}
