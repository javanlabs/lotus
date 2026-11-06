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
	// Trends are repo.OrgDailyTrends for homeTrendCombine; TrendKeys are those
	// keys' catalog rows, for direction and unit.
	Trends    map[string][]repo.Point
	TrendKeys map[string]repo.MetricKey

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

// homeAlert is a problem the health map cannot show: a broken connection,
// a binding that keeps failing to sync, a severe open issue. A failing or
// stale component is not one, because the map already marks it. at orders
// alerts newest first.
type homeAlert struct {
	Tone, Title, Detail, Href, When string
	at                              time.Time
	// outage leads the list: a broken connection stops every binding on it,
	// and its cause is often the same one behind the rest of the list.
	outage bool
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
	Viz       homeViz
	// Filter is the All / tag chips; the page's project-scoped panels are
	// already narrowed to its selection.
	Filter tagFilter
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
		kinds := cm.Metrics()
		if cm.StatusValue() == collector.StatusFail {
			failing++
		}
		for _, k := range bindableKinds {
			kh, ok := kinds[k]
			if ok && staleNote(k, kindSyncedAt(cm, kh), in.Now) != "" {
				stale++
			}
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
		a := homeAlert{Tone: "fail", Title: title, Detail: integrationProviderLabel(cn.Provider), Href: "/settings/connections", outage: true}
		if cn.Status == "auth_failed" {
			a.Detail += " · syncs are paused until its credential is replaced"
		}
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
		if alerts[i].outage != alerts[j].outage {
			return alerts[i].outage
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
		{Label: "NEED ATTENTION", Value: thousands(int64(failing)), Tone: attention(failing), Href: "/projects"},
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
	d.Viz = buildHomeViz(in, d.Rates)

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
		filter, err := loadTagFilter(c, s, "/dashboard")
		if err != nil {
			return writeRepoErr(c, err)
		}
		// keep is nil for All. Connections are org-wide and the QA workload
		// and activity feed are about people, so those stay unfiltered.
		keep, err := filter.projectFilter(ctx, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		kept := func(projectID uuid.UUID) bool { return keep == nil || keep[projectID] }
		for _, pr := range projects {
			if kept(pr.ID) {
				in.ProjectCount++
			}
		}
		if in.Components, err = repo.ListLiveComponents(ctx, s); err != nil {
			return writeRepoErr(c, err)
		}
		in.Components = slices.DeleteFunc(in.Components, func(lc repo.LiveComponent) bool { return !kept(lc.ProjectID) })
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
		in.FailingBindings = slices.DeleteFunc(in.FailingBindings, func(b repo.FailingBinding) bool { return !kept(b.ProjectID) })

		keepIDs := projectIDList(keep)
		if in.Trends, err = repo.OrgDailyTrends(ctx, s, homeTrendCombine(), homeTrendDays, now, keepIDs); err != nil {
			return writeRepoErr(c, err)
		}
		in.TrendKeys = map[string]repo.MetricKey{}
		for _, kind := range bindableKinds {
			keys, err := repo.ListMetricKeys(ctx, s, kind)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, k := range keys {
				in.TrendKeys[k.Key] = k
			}
		}

		if in.CanQA {
			summaries, err := repo.ListQAProjectSummaries(ctx, s, keepIDs)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, r := range summaries {
				in.OpenIssues += r.OpenIssues
				in.OpenPlans += r.OpenPlans
			}
			if in.AssignedToMe, err = repo.CountMyQAItems(ctx, s, p.UserID, keepIDs); err != nil {
				return writeRepoErr(c, err)
			}
			if in.SevereIssues, err = repo.ListOpenQAIssuesBySeverity(ctx, s, severeIssueSeverities); err != nil {
				return writeRepoErr(c, err)
			}
			in.SevereIssues = slices.DeleteFunc(in.SevereIssues, func(is repo.QAIssueSummary) bool { return !kept(is.ProjectID) })
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

		dash := buildHomeDashboard(in)
		dash.Filter = filter
		c.Type("html")
		return renderPage(c, d, "pages/dashboard.html", httpx.ViewData{
			Title: "Dashboard", Org: org, User: user, Nav: buildNav("dashboard", len(projects)),
			Data: dash,
		})
	}
}
