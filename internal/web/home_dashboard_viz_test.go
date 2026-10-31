package web

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func vizComponent(t *testing.T, in *homeInputs, name, project, status string, kinds map[string]string) {
	t.Helper()
	id := uuid.New()
	in.Components = append(in.Components, repo.LiveComponent{ID: id, Name: name, Slug: name, ProjectName: project, ProjectSlug: project})
	m := map[string]repo.KindHeadline{}
	for k, s := range kinds {
		m[k] = repo.KindHeadline{Status: s}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if in.Metrics == nil {
		in.Metrics = map[uuid.UUID]repo.ComponentMetric{}
	}
	in.Metrics[id] = repo.ComponentMetric{ComponentID: id, Status: status, MetricsJSON: raw}
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
	if got := headlineText(kh); got != "not scanned" {
		t.Errorf("headlineText = %q", got)
	}
	if got := homeMapCellLabel("trivy", kh); got != "not scanned" {
		t.Errorf("homeMapCellLabel = %q", got)
	}
}
