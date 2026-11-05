package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
)

// The scoring page's worked example is computed by hand; this computes it
// with the real formulas and rollups, so the page cannot drift from what
// Lotus actually shows.
func TestScoringDocs_WorkedExampleMatchesTheCode(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	// payment-api, from the formulas the page quotes.
	sonar := scoring.SonarScore(scoring.SonarInput{Coverage: f(90), WorstRating: "A", QualityGate: "ERROR"})
	trivy := scoring.TrivyScore(scoring.TrivyInput{Critical: 1, High: 5, PolicyFails: true})
	sentry := scoring.SentryScore(i(80), 5.6, 4)
	gitlab := scoring.GitLabScore(scoring.GitLabInput{SuccessRate: f(0.85), BranchesTotal: i(10), BranchesStale: i(3), LastFinished: "success"})
	webGitlab := scoring.GitLabScore(scoring.GitLabInput{SuccessRate: f(1), BranchesTotal: i(10), BranchesStale: i(1)})
	for name, c := range map[string]struct {
		got  *int
		want int
	}{"sonarqube": {sonar.Score, 48}, "trivy": {trivy.Score, 49}, "sentry": {sentry.Score, 80}, "gitlab": {gitlab.Score, 79}, "web-app gitlab": {webGitlab.Score, 98}} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s = %v, want %d", name, c.got, c.want)
		}
	}

	comp := func(kinds map[string]*int) repo.ComponentMetric {
		m := map[string]repo.KindHeadline{}
		for k, s := range kinds {
			m[k] = repo.KindHeadline{Score: s}
		}
		_, score := repo.ComponentRollup(m)
		b, _ := json.Marshal(m)
		return repo.ComponentMetric{MetricsJSON: b, Score: score}
	}
	components := []repo.ComponentMetric{
		comp(map[string]*int{"sonarqube": i(48), "trivy": i(49), "sentry": i(80), "gitlab": i(79)}),
		comp(map[string]*int{"sonarqube": i(95), "trivy": i(100), "sentry": nil, "gitlab": i(98)}),
		comp(map[string]*int{"trivy": i(100), "sentry": i(96), "gitlab": nil}),
	}
	for n, want := range []int{64, 98, 98} {
		if s := components[n].Score; s == nil || *s != want {
			t.Errorf("component %d = %v, want %d", n, s, want)
		}
	}
	roll := repo.RollupProject(components)
	if roll.Score == nil || *roll.Score != 83 || roll.NeedAttention != 1 || roll.Components != 3 {
		t.Fatalf("project = %v, %d of %d need attention; want 83, 1 of 3", roll.Score, roll.NeedAttention, roll.Components)
	}
	for kind, want := range map[string]int{"sonarqube": 72, "trivy": 83, "sentry": 88, "gitlab": 89} {
		if k := roll.Kinds[kind]; k.Score == nil || *k.Score != want {
			t.Errorf("%s tab = %v, want %d", kind, k.Score, want)
		}
	}

	page, err := os.ReadFile("../../web/templates/pages/scoring.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"= 82.75 →", ">83</strong> · Healthy", "1 of 3 components need attention",
		sonar.Breakdown, gitlab.Breakdown, "Healthy</strong>", "Needs attention</strong>"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("scoring page lacks %q", want)
		}
	}
	for _, gone := range []string{"amber", "grade —", "A is 90+"} {
		if strings.Contains(string(page), gone) {
			t.Errorf("scoring page still says %q", gone)
		}
	}
}
