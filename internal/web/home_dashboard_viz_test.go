package web

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// vizComponent adds a component whose kinds are given by status; the map
// reads scores, so a passing kind scores 90 and a failing one 40, and the
// component's own score is their rollup (status names the intent).
func vizComponent(t *testing.T, in *homeInputs, name, project, status string, kinds map[string]string) {
	t.Helper()
	id := uuid.New()
	in.Components = append(in.Components, repo.LiveComponent{ID: id, Name: name, Slug: name, ProjectName: project, ProjectSlug: project})
	m := map[string]repo.KindHeadline{}
	for k, s := range kinds {
		score := map[string]int{"pass": 90, "fail": 40}[s]
		m[k] = repo.KindHeadline{Status: s, Score: &score}
	}
	_, compScore := repo.ComponentRollup(m)
	if status == "fail" && compScore != nil && *compScore >= 80 {
		t.Fatalf("%s: a failing component must score below 80", name)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if in.Metrics == nil {
		in.Metrics = map[uuid.UUID]repo.ComponentMetric{}
	}
	in.Metrics[id] = repo.ComponentMetric{ComponentID: id, Status: status, Score: compScore, MetricsJSON: raw}
}

func vizFixture(t *testing.T) homeViz {
	t.Helper()
	in := homeInputs{Now: time.Now()}
	vizComponent(t, &in, "green", "alpha", "pass", map[string]string{"sonarqube": "pass", "gitlab": "pass"})
	vizComponent(t, &in, "near", "alpha", "fail", map[string]string{"sonarqube": "fail", "gitlab": "pass"})
	vizComponent(t, &in, "worst", "beta", "fail", map[string]string{"sonarqube": "fail", "gitlab": "fail"})
	in.Components = append(in.Components, repo.LiveComponent{ID: uuid.New(), Name: "silent", Slug: "silent", ProjectName: "beta", ProjectSlug: "beta"})
	return buildHomeHealthForTest(in)
}

func buildHomeHealthForTest(in homeInputs) homeViz { return buildHomeDashboard(in).Viz }

func TestHomeViz_HealthCountsEveryComponentOnce(t *testing.T) {
	h := vizFixture(t).Health
	if h.Pass != 1 || h.Fail != 2 || h.None != 1 || h.Total != 4 {
		t.Fatalf("health = %+v", h)
	}
	// Unmeasured components are not counted as unhealthy.
	if h.Pct != "33%" {
		t.Errorf("Pct = %q, want 33%% (1 of 3 measured)", h.Pct)
	}
}

func TestHomeViz_MapIsWorstFirstInTheFixedKindOrder(t *testing.T) {
	v := vizFixture(t)
	if v.Map[0].Name != "worst" || v.Map[1].Name != "near" {
		t.Fatalf("order = %s, %s", v.Map[0].Name, v.Map[1].Name)
	}
	if len(v.Map[0].Cells) != len(bindableKinds) {
		t.Fatalf("cells = %d", len(v.Map[0].Cells))
	}
	// bindableKinds: sonarqube, trivy, sentry, gitlab (AGENTS.md).
	if v.Map[0].Cells[0].Tone != "fail" || v.Map[0].Cells[1].Tone != "none" || v.Map[0].Cells[1].Href != "" {
		t.Errorf("cells = %+v", v.Map[0].Cells)
	}
	if v.Map[0].Cells[0].Label == "" {
		t.Error("a cell with no text would be colour alone")
	}
	// The project name under a component opens the project.
	if v.Map[0].ProjectHref != "/projects/beta" || v.Map[1].ProjectHref != "/projects/alpha" {
		t.Errorf("project links = %q, %q", v.Map[0].ProjectHref, v.Map[1].ProjectHref)
	}
}

// The map is the only list of failing components, so past its cap it drops
// only components with nothing failing, and its "healthier" note stays true.
func TestHomeViz_MapNeverHidesAFailingComponent(t *testing.T) {
	in := homeInputs{Now: time.Now()}
	failing := homeHealthMapCap + 3
	for i := 0; i < failing; i++ {
		vizComponent(t, &in, fmt.Sprintf("bad%d", i), "alpha", "fail", map[string]string{"sonarqube": "fail"})
	}
	for i := 0; i < 2; i++ {
		vizComponent(t, &in, fmt.Sprintf("good%d", i), "alpha", "pass", map[string]string{"sonarqube": "pass"})
	}
	v := buildHomeHealthForTest(in)
	if len(v.Map) != failing || v.MoreMap != 2 {
		t.Fatalf("map lists %d and %d more, want %d and 2", len(v.Map), v.MoreMap, failing)
	}
}

func TestHomeViz_InsightsNameTheBiggestCauseAndTheNearWin(t *testing.T) {
	ins := vizFixture(t).Insights
	if ins[0].Figure != "2/3" || ins[0].Title != "SonarQube causes the most failures" {
		t.Errorf("biggest = %+v", ins[0])
	}
	if ins[1].Figure != "1" || len(ins[1].Links) != 1 || ins[1].Links[0].Href != "/projects/alpha/components/near/metrics/sonarqube" {
		t.Errorf("near win = %+v", ins[1])
	}
	if ins[2].Title != "beta is the weakest project" {
		t.Errorf("weakest = %+v", ins[2])
	}
}

func TestHomeViz_NothingFailingSaysSo(t *testing.T) {
	in := homeInputs{Now: time.Now()}
	vizComponent(t, &in, "green", "alpha", "pass", map[string]string{"sonarqube": "pass"})
	ins := buildHomeDashboard(in).Viz.Insights
	if len(ins) != 1 || ins[0].Tone != "pass" {
		t.Fatalf("insights = %+v", ins)
	}
}

func TestHomeTrends_DeltaFollowsTheMetricsDirection(t *testing.T) {
	day := func(i int, v float64) repo.Point {
		return repo.Point{At: time.Date(2026, 10, i, 0, 0, 0, 0, time.UTC), Value: v, HasValue: true}
	}
	key := primaryChartKey("trivy")
	in := homeInputs{
		Trends:    map[string][]repo.Point{key: {{}, day(2, 10), day(3, 25)}},
		TrendKeys: map[string]repo.MetricKey{key: {Direction: "lower_better", Unit: "count"}},
	}
	var got homeTrend
	for _, tr := range homeTrends(in) {
		if tr.Label == homeTrendDefs["trivy"].Label {
			got = tr
		}
	}
	if got.Delta != "+15" || got.DeltaTone != "fail" {
		t.Errorf("more findings: Delta=%q tone=%q", got.Delta, got.DeltaTone)
	}
}

func TestHomeInsights_NamesTheTrendThatGotWorstRelativeToItsStart(t *testing.T) {
	in := homeInputs{Now: time.Now()}
	vizComponent(t, &in, "bad", "alpha", "fail", map[string]string{"sonarqube": "fail"})
	v := buildHomeDashboard(in).Viz
	trends := append(v.Trends[:0:0], homeTrend{Label: "AVG COVERAGE", Delta: "-0.9%", DeltaTone: "fail", change: 0.01},
		homeTrend{Label: "AVG HEALTH SCORE", Delta: "-37", DeltaTone: "fail", change: 0.45},
		homeTrend{Label: "AVG PIPELINE SUCCESS", Delta: "+2.9%", DeltaTone: "pass", change: 0.9})
	ins := homeInsights(1, map[string]int{"sonarqube": 1}, map[string]map[string]int{}, nil, nil,
		func() (string, int, bool) { return "", 0, false }, trends)
	last := ins[len(ins)-1]
	if last.Title != "Avg health score got worse" || last.Figure != "-37" {
		t.Errorf("worst trend insight = %+v", last)
	}
}

func TestHomeViz_AValueNotSyncedLatelyIsMarkedStale(t *testing.T) {
	now := time.Now()
	in := homeInputs{Now: now}
	id := uuid.New()
	in.Components = []repo.LiveComponent{{ID: id, Name: "c", Slug: "c", ProjectName: "p", ProjectSlug: "p"}}
	old, recent := now.Add(-30*24*time.Hour), now.Add(-time.Hour)
	raw, err := json.Marshal(map[string]repo.KindHeadline{
		"trivy":  {Status: "pass", UpdatedAt: &old},
		"gitlab": {Status: "pass", UpdatedAt: &recent},
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Metrics = map[uuid.UUID]repo.ComponentMetric{id: {ComponentID: id, Status: "pass", MetricsJSON: raw}}
	cells := map[string]homeMapCell{}
	for i, c := range buildHomeDashboard(in).Viz.Map[0].Cells {
		cells[bindableKinds[i]] = c
	}
	if !cells["trivy"].Stale || cells["gitlab"].Stale {
		t.Errorf("trivy stale=%v gitlab stale=%v", cells["trivy"].Stale, cells["gitlab"].Stale)
	}
}

// A state with nothing measured names itself in both places a headline shows
// up short, rather than reading as a count of zero.
func TestStateLabelBeatsTheCount(t *testing.T) {
	zero := int64(0)
	kh := repo.KindHeadline{Status: "unknown", StateLabel: "not scanned", CountValue: &zero, CountLabel: "finding"}
	if got := headlineText(kh); got != "Not scanned" {
		t.Errorf("headlineText = %q", got)
	}
	// On the map a cell with no score is a grey "–", and the reason is
	// its hover text.
	if got := homeMapCellLabel(kh); got != "–" {
		t.Errorf("homeMapCellLabel = %q", got)
	}
	if got := homeMapCellTitle("trivy", kh); got != "Trivy: Not scanned" {
		t.Errorf("homeMapCellTitle = %q", got)
	}
}

// A map cell is the kind's score on the 0–100 scale, coloured by its
// band; Trivy's counts and SonarQube's own letter move to the title.
func TestHomeMapCellIsTheScore(t *testing.T) {
	trivy := repo.KindHeadline{Status: "fail", Score: intPtr(49), CountValue: int64Ptr(31), CountLabel: "finding", Summary: "1 critical, 5 high, 31 total"}
	if got := homeMapCellLabel(trivy); got != "49" {
		t.Errorf("trivy label = %q, want the score", got)
	}
	if got := homeMapCellTitle("trivy", trivy); got != "Trivy: 49 · Fail · 1 critical · 5 high · 31 total" {
		t.Errorf("trivy title = %q", got)
	}
	sonar := repo.KindHeadline{Status: "pass", Score: intPtr(96), Grade: "A"}
	if got := homeMapCellLabel(sonar); got != "96" {
		t.Errorf("sonarqube label = %q, want the score, not the letter", got)
	}
	if got := homeMapCellTitle("sonarqube", sonar); got != "SonarQube: 96 · Pass · SonarQube worst rating A" {
		t.Errorf("sonarqube title = %q", got)
	}
}
