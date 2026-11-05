package web

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// metricTabRe matches one tab: its key, then either a link (its href and
// whether it is the current page) or the non-link an unbound kind draws.
var metricTabRe = regexp.MustCompile(`data-tab="([^"]*)">\s*(?:<a href="([^"]*)" class="lotus-mt-link"( aria-current="page")?|<div class="lotus-mt-link" aria-disabled="true">)`)

// tabWant is one tab a strip must draw, in order; href "" is a tab that
// is not a link.
type tabWant struct{ key, href string }

// projectTabWants is a project's strip: the score, then one tab per kind
// in bindableKinds order (AGENTS.md), each opening its own page.
func projectTabWants(base string) []tabWant {
	want := []tabWant{{"score", base}}
	for _, k := range bindableKinds {
		want = append(want, tabWant{k, base + "/metrics/" + k})
	}
	return want
}

// assertTabs checks a rendered strip has exactly want's tabs, in order,
// with only active marked as the current page.
func assertTabs(t *testing.T, html string, want []tabWant, active string) {
	t.Helper()
	got := metricTabRe.FindAllStringSubmatch(html, -1)
	if len(got) != len(want) {
		t.Fatalf("%d metric tabs, want %d (%v)", len(got), len(want), want)
	}
	for i, w := range want {
		if got[i][1] != w.key || got[i][2] != w.href {
			t.Errorf("tab %d = %s → %q, want %s → %q", i, got[i][1], got[i][2], w.key, w.href)
		}
		if current := got[i][3] != ""; current != (w.key == active) {
			t.Errorf("tab %s current = %v, want %v", w.key, current, w.key == active)
		}
	}
}

// assertMetricTabs is assertTabs for a project's strip.
func assertMetricTabs(t *testing.T, html, base, active string) {
	t.Helper()
	assertTabs(t, html, projectTabWants(base), active)
}

// assertStatsBeneathTabs checks the panel sits after the last tab and
// before the page's table.
func assertStatsBeneathTabs(t *testing.T, html string) {
	t.Helper()
	locs := metricTabRe.FindAllStringIndex(html, -1)
	stats := strings.Index(html, `class="lotus-mt-stats"`)
	if len(locs) == 0 || stats < 0 {
		t.Fatalf("no tabs (%d) or no stats panel (%d)", len(locs), stats)
	}
	if last := locs[len(locs)-1][0]; stats < last {
		t.Errorf("stats panel at %d is before the last tab at %d", stats, last)
	}
	if grid := strings.Index(html, "lotus-pm-grid"); grid >= 0 && stats > grid {
		t.Errorf("stats panel at %d is after the table at %d", stats, grid)
	}
}

func renderMetricTabs(t *testing.T, data map[string]any) string {
	t.Helper()
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	var out bytes.Buffer
	if err := r.RenderNamed(&out, "metric_tabs", data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The project's strip is the same on the project page and on every
// integration page; only the open tab changes, and the page's other
// numbers sit under it.
func TestMetricTabs_SameStripOnEveryPage(t *testing.T) {
	t.Parallel()
	project := &repo.Project{ID: uuid.New(), Slug: "payments"}
	comp := uuid.New()
	score := 80
	components := []componentInfo{{ID: comp, Name: "api", ProjectID: project.ID, ProjectSlug: "payments"}}
	metrics := map[uuid.UUID]repo.ComponentMetric{comp: {
		ComponentID: comp, ProjectID: project.ID, Status: "pass", Score: &score,
		MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: intPtr(80)}}),
	}}
	tabs := projectTabs(projectSummary(project, components, metrics))
	stats := []kindStat{{Label: "BUGS", Value: "3", Tone: "fail"}, {Label: "LINES OF CODE", Value: "20,000", Tone: "none"}}

	for _, active := range append([]string{"score"}, bindableKinds...) {
		t.Run(active, func(t *testing.T) {
			data := map[string]any{"Active": active, "Tabs": tabs}
			if active != "score" {
				data["Stats"] = stats
			}
			html := renderMetricTabs(t, data)
			assertMetricTabs(t, html, "/projects/payments", active)

			// The open tab's number is the page's headline.
			if n := strings.Count(html, "data-headline"); n != 1 {
				t.Errorf("%d headline markers, want 1", n)
			}
			// Every project tab explains its number.
			if n := strings.Count(html, "How this number is calculated"); n != len(tabs) {
				t.Errorf("%d info buttons, want %d", n, len(tabs))
			}
			if active == "score" {
				if strings.Contains(html, "lotus-mt-stats") {
					t.Error("the score tab drew a stats panel")
				}
				return
			}
			assertStatsBeneathTabs(t, html)
			for _, s := range stats {
				if !strings.Contains(html, s.Label) || !strings.Contains(html, s.Value) {
					t.Errorf("stat %s missing from the panel", s.Label)
				}
			}
		})
	}
}

// The project page's tabs and an integration page's tabs read one
// computation, so a tab cannot show a different number on the page it opens
// than on the page it came from.
func TestProjectSummary_MatchesTheProjectPage(t *testing.T) {
	t.Parallel()
	project := &repo.Project{ID: uuid.New(), Slug: "payments"}
	var components []componentInfo
	metrics := map[uuid.UUID]repo.ComponentMetric{}
	for i, kinds := range []map[string]repo.KindHeadline{
		{"sonarqube": {Status: "pass", Score: intPtr(90)}, "gitlab": {Status: "fail", Score: intPtr(40)}},
		{"sonarqube": {Status: "fail", Score: intPtr(30)}},
		{},
	} {
		id := uuid.New()
		components = append(components, componentInfo{ID: id, Name: string(rune('a' + i)), ProjectID: project.ID, ProjectSlug: "payments"})
		if len(kinds) == 0 {
			continue // never synced: no projection row
		}
		score := 60
		metrics[id] = repo.ComponentMetric{ComponentID: id, ProjectID: project.ID, Status: "pass", Score: &score, MetricsJSON: metricsJSON(t, kinds)}
	}

	vm := buildDashboardViewModel("", 1, components, metrics)
	sc, kpis := projectSummary(project, components, metrics)
	if !reflect.DeepEqual(vm.Score, sc) || !reflect.DeepEqual(vm.KPIs, kpis) {
		t.Fatalf("project page tabs %+v / %+v, metric page tabs %+v / %+v", vm.Score, vm.KPIs, sc, kpis)
	}
	if !reflect.DeepEqual(vm.MetricTabs(), projectTabs(sc, kpis)) {
		t.Fatal("the project page's strip differs from an integration page's")
	}
	if sc.Href != "/projects/payments" {
		t.Errorf("score tab opens %q, want the project page", sc.Href)
	}
	for _, k := range kpis {
		if k.Href != "/projects/payments/metrics/"+k.Key {
			t.Errorf("%s tab opens %q", k.Key, k.Href)
		}
	}
}

// componentTabWants is a component's strip: one tab per kind in
// bindableKinds order — a bound kind opening its metric page, an unbound
// one not a link.
func componentTabWants(base string, bound ...string) []tabWant {
	var want []tabWant
	for _, k := range bindableKinds {
		href := ""
		for _, b := range bound {
			if b == k {
				href = base + "/metrics/" + k
			}
		}
		want = append(want, tabWant{k, href})
	}
	return want
}

// A component's strip: every kind in the fixed order, bound or not; a
// bound kind shows the same headline the project's component table shows
// for it, and says when it is stale or disabled; an unbound one opens
// nothing.
func TestComponentTabs(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	dashboardNow = func() time.Time { return now }
	t.Cleanup(func() { dashboardNow = time.Now })

	project := &repo.Project{ID: uuid.New(), Slug: "alurkerja"}
	comp := &repo.Component{ID: uuid.New(), Slug: "app-react-v2"}
	base := "/projects/alurkerja/components/app-react-v2"
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	headlines := map[string]repo.KindHeadline{
		"sonarqube": {Status: "pass", Score: intPtr(92), CountValue: int64Ptr(0), CountLabel: "bugs + vulnerabilities", UpdatedAt: at(time.Hour)},
		"trivy":     {Status: "fail", CountValue: int64Ptr(3), CountLabel: "critical finding", UpdatedAt: at(3 * 24 * time.Hour)},
	}
	score := 71
	metric := &repo.ComponentMetric{ComponentID: comp.ID, Status: "fail", Score: &score, UpdatedAt: now, MetricsJSON: metricsJSON(t, headlines)}
	bindings := []repo.Binding{
		{Kind: "trivy", Enabled: true},
		{Kind: "sonarqube", Enabled: true},
		{Kind: "gitlab", Enabled: false}, // bound, never synced, disabled
	}

	tabs := componentTabs(project, comp, bindings, metric)
	byKey := map[string]metricTab{}
	for _, tab := range tabs {
		byKey[tab.Key] = tab
	}

	html := renderMetricTabs(t, map[string]any{"Active": "sonarqube", "Tabs": tabs})
	assertTabs(t, html, componentTabWants(base, "sonarqube", "trivy", "gitlab"), "sonarqube")
	// The same cell the project's component table draws for this kind.
	cell := badgeFor(metric.Metrics(), "sonarqube")
	if s := byKey["sonarqube"]; s.Value != cell.Text || s.ValueTone != cell.Tone || s.Sub != cell.Title || s.Unit != "/100" || s.Title != "" {
		t.Errorf("sonarqube = %+v, want the table cell %+v", s, cell)
	}
	// No score: a grey "–", whatever the stored status says — until it is
	// stale, which the note says and the hover explains.
	if tv := byKey["trivy"]; tv.Value != "–" || tv.ValueTone != "none" || tv.Verdict != nil || !strings.HasPrefix(tv.Sub, "Stale · synced 3 days ago") ||
		!strings.Contains(tv.Title, "Trivy has not synced") {
		t.Errorf("trivy = %+v", tv)
	}
	if g := byKey["gitlab"]; g.Value != "–" || g.Sub != "Disabled · Not synced yet" || g.Muted {
		t.Errorf("gitlab = %+v", g)
	}
	if s := byKey["sentry"]; !s.Muted || s.Sub != "Not bound" || s.Value != "–" || s.Href != "" {
		t.Errorf("sentry = %+v", s)
	}
	if !strings.Contains(html, `data-tab="sentry"`) || strings.Count(html, "is-muted") != 1 {
		t.Errorf("want exactly the unbound sentry tab muted")
	}
	// Component tabs carry no info popover: there is no rate to explain.
	if strings.Contains(html, "How this number is calculated") {
		t.Error("a component tab drew an info button")
	}

	// No letter grade beside any score (43e3b61 reversed): every tab reads
	// 0–100, coloured by its band.
	sentryHeadlines := map[string]repo.KindHeadline{
		"sentry":    {Status: "fail", Score: intPtr(17), Grade: "E", UpdatedAt: at(time.Hour)},
		"sonarqube": {Status: "pass", Score: intPtr(92), Grade: "A", UpdatedAt: at(time.Hour)},
	}
	graded := componentTabs(project, comp, []repo.Binding{{Kind: "sentry", Enabled: true}, {Kind: "sonarqube", Enabled: true}},
		&repo.ComponentMetric{ComponentID: comp.ID, UpdatedAt: now, MetricsJSON: metricsJSON(t, sentryHeadlines)})
	if html := renderMetricTabs(t, map[string]any{"Active": "sentry", "Tabs": graded}); strings.Contains(html, "Grade") || strings.Contains(html, ">E<") {
		t.Errorf("the sentry tab still shows a letter grade:\n%s", html)
	}

	// Before any sync, and with nothing bound, the strip keeps its shape —
	// every tab drawn, none a link.
	empty := componentTabs(project, comp, nil, nil)
	assertTabs(t, renderMetricTabs(t, map[string]any{"Active": "", "Tabs": empty}), componentTabWants(base), "")
}

// A component opens on its first bound kind in the fixed order — SonarQube
// when it has one — whatever order the bindings were made in.
func TestFirstBoundKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kinds []string
		want  string
	}{
		{nil, ""},
		{[]string{"gitlab", "sonarqube"}, "sonarqube"},
		{[]string{"gitlab", "sentry"}, "sentry"},
		{[]string{"gitlab"}, "gitlab"},
	} {
		var bindings []repo.Binding
		for _, k := range c.kinds {
			bindings = append(bindings, repo.Binding{Kind: k})
		}
		if got := firstBoundKind(bindings); got != c.want {
			t.Errorf("firstBoundKind(%v) = %q, want %q", c.kinds, got, c.want)
		}
	}
}

// A score's chip is its band, in a word: the same line the number is
// coloured by.
func TestBandVerdict(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		score int
		want  tabVerdict
	}{
		{100, tabVerdict{Tone: "pass", Text: "Healthy"}},
		{80, tabVerdict{Tone: "pass", Text: "Healthy"}},
		{79, tabVerdict{Tone: "fail", Text: "Needs attention"}},
		{0, tabVerdict{Tone: "fail", Text: "Needs attention"}},
	} {
		got := bandVerdict(&c.score)
		if *got != c.want {
			t.Errorf("bandVerdict(%d) = %+v, want %+v", c.score, *got, c.want)
		}
		if got.Tone != bandTone(&c.score) {
			t.Errorf("bandVerdict(%d) tone %s, but the number is %s", c.score, got.Tone, bandTone(&c.score))
		}
	}
	if bandVerdict(nil) != nil {
		t.Error("no score draws no chip")
	}
}

// The project strip's chips: the score carries its band and the count of
// components needing attention; each kind's average carries its band; a
// kind with no data draws an empty slot rather than a verdict.
func TestProjectTabs_Verdicts(t *testing.T) {
	t.Parallel()
	kpis := []kpiCard{
		{Key: "sonarqube", Value: "50", Unit: "/100", Tone: "fail", Score: intPtr(50)},
		{Key: "trivy", Value: "100", Unit: "/100", Tone: "pass", Score: intPtr(100)},
		{Key: "sentry", Value: "–", Tone: "none"},
		{Key: "gitlab", Value: "80", Unit: "/100", Tone: "pass", Score: intPtr(80)},
	}
	for _, c := range []struct {
		name  string
		score scoreCard
		want  *tabVerdict
		sub   string
	}{
		{"needs attention", scoreCard{HasScore: true, Score: 71, Tone: "fail", Components: 7, NeedAttention: 2}, &tabVerdict{Tone: "fail", Text: "Needs attention"}, "2 of 7 components need attention"},
		{"healthy", scoreCard{HasScore: true, Score: 84, Tone: "pass", Components: 3, NeedAttention: 1}, &tabVerdict{Tone: "pass", Text: "Healthy"}, "1 of 3 components need attention"},
		{"empty", scoreCard{}, nil, "no data yet"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tabs := projectTabs(c.score, kpis)
			if !reflect.DeepEqual(tabs[0].Verdict, c.want) || tabs[0].Sub != c.sub {
				t.Errorf("score tab verdict %+v sub %q, want %+v %q", tabs[0].Verdict, tabs[0].Sub, c.want, c.sub)
			}
			want := map[string]*tabVerdict{
				"sonarqube": {Tone: "fail", Text: "Needs attention"},
				"trivy":     {Tone: "pass", Text: "Healthy"},
				"sentry":    nil,
				"gitlab":    {Tone: "pass", Text: "Healthy"},
			}
			for _, tab := range tabs[1:] {
				if !reflect.DeepEqual(tab.Verdict, want[tab.Key]) {
					t.Errorf("%s verdict %+v, want %+v", tab.Key, tab.Verdict, want[tab.Key])
				}
			}
		})
	}
}

// A component tab's chip carries the verdict of a scored kind — dashed
// once stale — and every tab, chip or not, draws the slot so the strip
// keeps one height.
func TestComponentTabs_Verdicts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	dashboardNow = func() time.Time { return now }
	t.Cleanup(func() { dashboardNow = time.Now })

	project := &repo.Project{ID: uuid.New(), Slug: "alurkerja"}
	comp := &repo.Component{ID: uuid.New(), Slug: "app"}
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	headlines := map[string]repo.KindHeadline{
		"sonarqube": {Status: "pass", Score: intPtr(96), UpdatedAt: at(time.Hour)},
		"trivy":     {Status: "fail", Score: intPtr(17), UpdatedAt: at(18 * 24 * time.Hour)},
		"sentry":    {Status: "fail", CountValue: int64Ptr(86), CountLabel: "issue", UpdatedAt: at(time.Hour)},
	}
	metric := &repo.ComponentMetric{ComponentID: comp.ID, Status: "fail", UpdatedAt: now, MetricsJSON: metricsJSON(t, headlines)}
	bindings := []repo.Binding{{Kind: "sonarqube", Enabled: true}, {Kind: "trivy", Enabled: false}, {Kind: "sentry", Enabled: true}}

	tabs := componentTabs(project, comp, bindings, metric)
	want := map[string]*tabVerdict{
		"sonarqube": {Tone: "pass", Text: "Healthy"},
		"trivy":     {Tone: "fail", Text: "Needs attention", Stale: true},
		"sentry":    nil, // no score: no band to name
		"gitlab":    nil, // unbound
	}
	for _, tab := range tabs {
		if !reflect.DeepEqual(tab.Verdict, want[tab.Key]) {
			t.Errorf("%s verdict %+v, want %+v", tab.Key, tab.Verdict, want[tab.Key])
		}
	}
	if tv := tabs[1]; tv.Sub != "Disabled · Stale · synced 18 days ago" {
		t.Errorf("trivy sub %q", tv.Sub)
	}
	// SonarQube said nothing but its status, which the chip already says:
	// the line under it says when it synced instead.
	if s := tabs[0]; s.Sub != "Synced 1 hr ago" {
		t.Errorf("sonarqube sub %q, want when it synced", s.Sub)
	}

	html := renderMetricTabs(t, map[string]any{"Active": "sonarqube", "Tabs": tabs})
	if n := strings.Count(html, `class="lotus-mt-verdict-slot"`); n != len(tabs) {
		t.Errorf("%d verdict slots, want one per tab (%d)", n, len(tabs))
	}
	for _, s := range []string{
		`lotus-mt-verdict lotus-mt-verdict-pass" data-verdict="pass">`,
		`lotus-mt-verdict lotus-mt-verdict-fail is-stale" data-verdict="fail">`,
		"Healthy</span>", "Needs attention</span>",
	} {
		if !strings.Contains(html, s) {
			t.Errorf("strip lacks %q", s)
		}
	}
	if strings.Contains(html, "lotus-mt-bar") {
		t.Error("the strip still draws a MeterBar")
	}
}
