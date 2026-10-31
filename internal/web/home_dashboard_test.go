package web

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

var homeNow = time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)

func homeComponent(project, name string) repo.LiveComponent {
	return repo.LiveComponent{ID: uuid.New(), Name: name, Slug: strings.ToLower(name), ProjectID: uuid.New(), ProjectName: project, ProjectSlug: strings.ToLower(project)}
}

func strPtrHome(s string) *string { return &s }

// Each problem the health map cannot show raises an alert, an outage first
// and then the newest. A failing or stale component is the map's to show, so
// it raises none, and is still counted in the figures.
func TestBuildHomeDashboard_AlertsAndFigures(t *testing.T) {
	t.Parallel()
	at := func(d time.Duration) *time.Time { v := homeNow.Add(-d); return &v }
	failing := homeComponent("AlurKerja", "Authentication")
	stale := homeComponent("AlurKerja", "Billing")
	healthy := homeComponent("Payments", "Ledger")
	metrics := map[uuid.UUID]repo.ComponentMetric{
		failing.ID: {Status: "fail", UpdatedAt: homeNow.Add(-time.Hour), MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sonarqube": {Status: "fail", UpdatedAt: at(time.Hour)},
			"gitlab":    {Status: "fail", UpdatedAt: at(time.Hour)},
			"trivy":     {Status: "pass", UpdatedAt: at(time.Hour)},
		})},
		stale.ID: {Status: "pass", UpdatedAt: homeNow, MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sentry": {Status: "pass", UpdatedAt: at(3 * 24 * time.Hour)},
		})},
		healthy.ID: {Status: "pass", UpdatedAt: homeNow, MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sonarqube": {Status: "pass", UpdatedAt: at(time.Minute)},
		})},
	}
	in := homeInputs{
		Now: homeNow, OrgName: "Javan", ProjectCount: 2,
		Components:  []repo.LiveComponent{failing, stale, healthy},
		Metrics:     metrics,
		FailedSyncs: 4,
		Connections: []repo.Connection{
			{Name: "sonar.internal", Provider: "sonarqube", Status: "auth_failed", LastError: strPtrHome("401 Unauthorized"), LastVerifiedAt: at(30 * time.Minute)},
			{Name: "gitlab.com", Provider: "gitlab", Status: "ok"},
		},
		FailingBindings: []repo.FailingBinding{{Kind: "gitlab", ConsecutiveFailures: 5, LiveComponent: healthy}},
		CanQA:           true, OpenIssues: 7, OpenPlans: 2, AssignedToMe: 3,
		SevereIssues: []repo.QAIssueSummary{{ProjectID: failing.ProjectID, ProjectName: "AlurKerja", ProjectSlug: "alurkerja",
			Key: 12, Title: "Login loops", Severity: "blocker", Status: "open", CreatedAt: homeNow.Add(-10 * time.Minute)}},
	}
	d := buildHomeDashboard(in)

	titles := make([]string, len(d.Alerts))
	for i, a := range d.Alerts {
		titles[i] = a.Tone + " " + a.Title
	}
	want := []string{
		"fail Connection sonar.internal cannot sign in", // an outage leads
		"fail Blocker issue #12: Login loops",           // 10 min ago
		"fail GitLab sync keeps failing on Ledger",      // no time: last of the failures
	}
	if strings.Join(titles, "\n") != strings.Join(want, "\n") {
		t.Fatalf("alerts =\n%s\nwant\n%s", strings.Join(titles, "\n"), strings.Join(want, "\n"))
	}
	// The binding alert opens its Syncs.
	if a := d.Alerts[2]; a.Href != "/projects/payments/components/ledger/metrics/gitlab/runs" || a.Detail != "Payments · 5 failed syncs in a row" {
		t.Errorf("failing binding alert = %+v", a)
	}
	if a := d.Alerts[0]; a.Detail != "SonarQube · syncs are paused until its credential is replaced · 401 Unauthorized" || a.Href != "/settings/connections" {
		t.Errorf("connection alert = %+v", a)
	}
	if a := d.Alerts[1]; a.Href != "/projects/alurkerja/qa/issues/12" {
		t.Errorf("issue alert = %+v", a)
	}

	figures := map[string]homeFigure{}
	for _, f := range d.Figures {
		figures[f.Label] = f
	}
	for label, value := range map[string]string{
		"PROJECTS": "2", "COMPONENTS": "3", "FAILING COMPONENTS": "1", "STALE METRICS": "1",
		"SYNC FAILURES · 24H": "4", "OPEN ISSUES": "7", "OPEN PLANS": "2", "ASSIGNED TO ME": "3",
	} {
		if figures[label].Value != value {
			t.Errorf("%s = %q, want %s", label, figures[label].Value, value)
		}
	}
	if figures["FAILING COMPONENTS"].Tone != "fail" || figures["PROJECTS"].Tone != "neutral" {
		t.Errorf("tones: failing %q, projects %q", figures["FAILING COMPONENTS"].Tone, figures["PROJECTS"].Tone)
	}
	// The per-integration rates, in the fixed order (AGENTS.md).
	for i, k := range bindableKinds {
		if d.Rates[i].Key != k {
			t.Fatalf("rates order = %+v", d.Rates)
		}
	}
	if d.Subtitle != "2 projects · 3 components across Javan" {
		t.Errorf("subtitle = %q", d.Subtitle)
	}
}

// Past the cap, the page lists the most severe and says how many more.
func TestBuildHomeDashboard_AlertsAreCapped(t *testing.T) {
	t.Parallel()
	var bindings []repo.FailingBinding
	for i := 0; i < homeAlertCap+4; i++ {
		bindings = append(bindings, repo.FailingBinding{Kind: "sonarqube", ConsecutiveFailures: 3, LiveComponent: homeComponent("P", fmt.Sprintf("c%d", i))})
	}
	d := buildHomeDashboard(homeInputs{Now: homeNow, FailingBindings: bindings})
	if len(d.Alerts) != homeAlertCap || d.MoreAlerts != 4 {
		t.Fatalf("%d alerts and %d more, want %d and 4", len(d.Alerts), d.MoreAlerts, homeAlertCap)
	}
}

// Without QA access the page shows none of QA's numbers, issues or lists.
func TestBuildHomeDashboard_NoQAWithoutQAAccess(t *testing.T) {
	t.Parallel()
	d := buildHomeDashboard(homeInputs{
		Now: homeNow, CanQA: false, OpenIssues: 9,
		SevereIssues: []repo.QAIssueSummary{{Key: 1, Severity: "blocker"}},
		Workload:     &repo.QAWorkload{Month: "2026-10"},
		Activity:     []repo.QAIssueActivity{{Kind: "reported"}},
	})
	if d.ShowQA || len(d.Tops) != 0 || len(d.Activity) != 0 || len(d.Alerts) != 0 {
		t.Fatalf("QA shown without access: %+v", d)
	}
	for _, f := range d.Figures {
		if f.Label == "OPEN ISSUES" {
			t.Fatal("open issues figure shown without QA access")
		}
	}
}

// The month's QA activity, ranked: most first, ties by name, nothing for a
// row with nothing to count, five at most; testers only for whoever may
// read the workload.
func TestHomeTops(t *testing.T) {
	t.Parallel()
	proj := func(name string, total, untested, cases, issues int) repo.QAWorkloadProject {
		return repo.QAWorkloadProject{ProjectID: uuid.New(), ProjectSlug: strings.ToLower(name), Name: name, CasesCreated: cases, IssuesCreated: issues,
			QAWorkloadCounts: repo.QAWorkloadCounts{Total: total, Untested: untested}}
	}
	w := &repo.QAWorkload{
		Month: "2026-10",
		Projects: []repo.QAWorkloadProject{
			proj("Beta", 40, 10, 3, 1), proj("Alpha", 30, 0, 1, 4), proj("Gamma", 10, 10, 0, 0),
			proj("Delta", 5, 0, 0, 4), proj("Eps", 4, 0, 0, 0), proj("Zeta", 3, 0, 0, 0), proj("Eta", 2, 0, 0, 0),
		},
		People: []repo.QAWorkloadPerson{
			{Name: "Rina", IssuesReported: 2, QAWorkloadCounts: repo.QAWorkloadCounts{Total: 20, Untested: 5}},
			{Name: "Budi", QAWorkloadCounts: repo.QAWorkloadCounts{Total: 3, Untested: 3}},
		},
	}
	names := func(rows []homeTopRow) string {
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%s:%d", r.Name, r.Value))
		}
		return strings.Join(out, " ")
	}

	tops := homeTops(w, false)
	if len(tops) != 2 {
		t.Fatalf("%d lists without workload access, want 2 (no testers)", len(tops))
	}
	// Gamma executed nothing (all untested): left out. Six projects ran
	// tests; five are shown.
	if got := names(tops[0].Rows); got != "Alpha:30 Beta:30 Delta:5 Eps:4 Zeta:3" {
		t.Errorf("tests executed = %s", got)
	}
	if tops[0].Rows[1].Note != "3 cases written" || tops[0].Rows[1].Href != "/projects/beta/qa" {
		t.Errorf("Beta row = %+v", tops[0].Rows[1])
	}
	if got := names(tops[1].Rows); got != "Alpha:4 Delta:4 Beta:1" {
		t.Errorf("issues reported = %s", got)
	}
	if tops[1].Rows[0].Href != "/projects/alpha/qa/issues" {
		t.Errorf("issues row links to %q", tops[1].Rows[0].Href)
	}

	tops = homeTops(w, true)
	if len(tops) != 3 || names(tops[2].Rows) != "Rina:15" || tops[2].Rows[0].Note != "2 issues reported" {
		t.Fatalf("testers = %+v", tops)
	}
	if empty := homeTops(&repo.QAWorkload{Month: "2026-10"}, true); len(empty[0].Rows) != 0 || empty[0].Empty == "" {
		t.Errorf("a month with no activity = %+v", empty[0])
	}
}

func TestHomeEventFor(t *testing.T) {
	t.Parallel()
	is := repo.QAIssueSummary{ProjectID: uuid.New(), ProjectName: "AlurKerja", ProjectSlug: "alurkerja", Key: 7, Title: "Typo", Severity: "minor"}
	cases := []struct {
		a              repo.QAIssueActivity
		verb, tone, by string
	}{
		{repo.QAIssueActivity{Kind: "reported", Actor: "Rina", QAIssueSummary: is}, "reported", "none", "Rina"},
		{repo.QAIssueActivity{Kind: "status", StatusTo: strPtrHome("fixed"), Actor: "Budi", QAIssueSummary: is}, "marked fixed", "pass", "Budi"},
		{repo.QAIssueActivity{Kind: "status", StatusTo: strPtrHome("reopened"), QAIssueSummary: is}, "marked reopened", "fail", "Someone"},
		{repo.QAIssueActivity{Kind: "comment", Actor: "Rina", QAIssueSummary: is}, "commented on", "none", "Rina"},
	}
	for _, c := range cases {
		c.a.At = homeNow.Add(-2 * time.Hour)
		e := homeEventFor(c.a, homeNow)
		if e.Verb != c.verb || e.Tone != c.tone || e.Actor != c.by || e.Issue != "#7 Typo" || e.Href != "/projects/alurkerja/qa/issues/7" || e.When != "2 hr ago" {
			t.Errorf("%s: %+v", c.a.Kind, e)
		}
	}
	// A blocker reported reads as one.
	blocker := is
	blocker.Severity = "blocker"
	if e := homeEventFor(repo.QAIssueActivity{Kind: "reported", QAIssueSummary: blocker}, homeNow); e.Tone != "fail" {
		t.Errorf("blocker report tone = %q", e.Tone)
	}
}

func TestMonthLabel(t *testing.T) {
	t.Parallel()
	if got := monthLabel("2026-10"); got != "October 2026" {
		t.Errorf("monthLabel = %q", got)
	}
}

// A broken connection stops every binding on it, and it has no timestamp to
// sort by. It used to sink below the cap under ten failing components, which
// is how an expired Harbor credential went unnoticed for a month.
func TestBuildHomeDashboard_ABrokenConnectionLeadsTheAlerts(t *testing.T) {
	t.Parallel()
	in := homeInputs{Now: homeNow}
	for i := 0; i < homeAlertCap+2; i++ {
		in.FailingBindings = append(in.FailingBindings, repo.FailingBinding{Kind: "sonarqube", ConsecutiveFailures: 3, LiveComponent: homeComponent("P", fmt.Sprintf("C%d", i))})
	}
	in.Connections = []repo.Connection{{Name: "Harbor", Provider: "harbor_trivy", Status: "auth_failed"}}
	first := buildHomeDashboard(in).Alerts[0]
	if !strings.Contains(first.Title, "Harbor cannot sign in") || !strings.Contains(first.Detail, "replaced") {
		t.Errorf("first alert = %+v", first)
	}
}
