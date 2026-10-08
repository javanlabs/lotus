package web

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

var metricTabLinkRe = regexp.MustCompile(`<a href="([^"]*)" class="lotus-mt-link"( aria-current="page")?>`)

// assertMetricTabs checks what every page of a project's metrics shares:
// the strip has the score tab and then one tab per kind in bindableKinds
// order (AGENTS.md), each opening its own page, with exactly the active
// one marked current. active is "score" or a kind.
func assertMetricTabs(t *testing.T, html, base, active string) {
	t.Helper()
	links := metricTabLinkRe.FindAllStringSubmatch(html, -1)
	want := []string{"score"}
	want = append(want, bindableKinds...)
	if len(links) != len(want) {
		t.Fatalf("%d metric tabs, want %d (%v)", len(links), len(want), want)
	}
	for i, tab := range want {
		href := base
		if tab != "score" {
			href = base + "/metrics/" + tab
		}
		if links[i][1] != href {
			t.Errorf("tab %d (%s) opens %q, want %q", i, tab, links[i][1], href)
		}
		if current := links[i][2] != ""; current != (tab == active) {
			t.Errorf("tab %s current = %v, want %v", tab, current, tab == active)
		}
	}
}

// assertStatsBeneathTabs checks the panel sits after the last tab and
// before the page's table.
func assertStatsBeneathTabs(t *testing.T, html string) {
	t.Helper()
	locs := metricTabLinkRe.FindAllStringIndex(html, -1)
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

// The strip is the same on the project page and on every integration page;
// only the open tab changes, and the page's other numbers sit under it.
func TestMetricTabs_SameStripOnEveryPage(t *testing.T) {
	project := &repo.Project{ID: uuid.New(), Slug: "payments"}
	comp := uuid.New()
	score := 80
	components := []componentInfo{{ID: comp, Name: "api", ProjectID: project.ID, ProjectSlug: "payments"}}
	metrics := map[uuid.UUID]repo.ComponentMetric{comp: {
		ComponentID: comp, ProjectID: project.ID, Status: "pass", Score: &score,
		MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{"sonarqube": {Status: "pass", Score: intPtr(80)}}),
	}}
	sc, kpis := projectSummary(project, components, metrics)
	stats := []kindStat{{Label: "BUGS", Value: "3", Tone: "fail"}, {Label: "LINES OF CODE", Value: "20,000", Tone: "none"}}

	for _, active := range append([]string{"score"}, bindableKinds...) {
		t.Run(active, func(t *testing.T) {
			data := map[string]any{"Active": active, "Score": sc, "KPIs": kpis}
			if active != "score" {
				data["Stats"] = stats
			}
			html := renderMetricTabs(t, data)
			assertMetricTabs(t, html, "/projects/payments", active)

			// The open tab's number is the page's headline.
			wantHeadline := 1
			if active == "score" {
				wantHeadline = 0
			}
			if n := strings.Count(html, "data-headline"); n != wantHeadline {
				t.Errorf("%d headline markers, want %d", n, wantHeadline)
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
	if sc.Href != "/projects/payments" {
		t.Errorf("score tab opens %q, want the project page", sc.Href)
	}
	for _, k := range kpis {
		if k.Href != "/projects/payments/metrics/"+k.Key {
			t.Errorf("%s tab opens %q", k.Key, k.Href)
		}
	}
}
