package collector_test

import (
	"fmt"
	"testing"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
	glcollector "github.com/uyab/lotus/internal/collector/gitlab"
	sentrycollector "github.com/uyab/lotus/internal/collector/sentry"
	sqcollector "github.com/uyab/lotus/internal/collector/sonarqube"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/scoring"
)

// TestStatusIsAlwaysDerivedFromScore is the invariant behind the score
// bands: for every kind, over a spread of inputs including each hard
// failure, the headline's status is exactly what its score reads as —
// pass at 80 or more, fail below, unknown with no score. A hard failure
// may only lower the number (a cap); it can never make a green number red.
func TestStatusIsAlwaysDerivedFromScore(t *testing.T) {
	var headlines []struct {
		name string
		h    collector.Headline
	}
	add := func(name string, h collector.Headline) {
		headlines = append(headlines, struct {
			name string
			h    collector.Headline
		}{name, h})
	}

	// SonarQube: coverage × worst rating × gate.
	for _, cov := range []*float64{nil, fp(0), fp(55.5), fp(79.4), fp(89.6), fp(100)} {
		for _, rating := range []string{"", "A", "B", "C", "D", "E"} {
			for _, gate := range []string{"", "OK", "WARN", "ERROR"} {
				m := &sonar.Measures{Coverage: cov, ReliabilityRating: rating, QualityGate: gate, Bugs: ip(0)}
				r, err := sqcollector.BuildImportedResult(m)
				if err != nil {
					t.Fatal(err)
				}
				add(fmt.Sprintf("sonarqube cov=%v rating=%q gate=%q", deref(cov), rating, gate), r.Headline)
			}
		}
	}

	// Trivy: counts × fail-on policy.
	for _, c := range []trivycollector.Counts{
		{}, {Total: 3, Low: 3}, {Total: 2, Medium: 2}, {Total: 1, High: 1}, {Total: 4, High: 4},
		{Total: 1, Critical: 1}, {Total: 31, Critical: 1, High: 5}, {Total: 9, Critical: 9},
	} {
		for _, failOn := range []string{"", "critical", "high", "any"} {
			add(fmt.Sprintf("trivy %+v fail_on=%q", c, failOn), trivycollector.ComputeHeadline(c, failOn))
		}
	}

	// Sentry: the ported score, rounded, and no signal.
	for _, s := range []*int{nil, ip(0), ip(19), ip(79), ip(80), ip(100)} {
		for _, issues := range []int{0, 4} {
			add(fmt.Sprintf("sentry score=%v issues=%d", deref(s), issues), sentrycollector.EnvironmentHeadline("all", s, 5.6, issues))
		}
	}

	// GitLab: pipelines × branches × last finished pipeline.
	for _, rate := range []*float64{nil, fp(0), fp(0.5), fp(0.85), fp(1)} {
		for _, stale := range []int{0, 3, 10} {
			for _, last := range []string{"", "success", "failed", "running"} {
				r, err := glcollector.BuildImportedResult(&glclient.Health{
					PipelineSuccessRate: rate, LastPipelineStatus: last,
					BranchesTotal: 10, BranchesStale: stale, BranchesActive: 10 - stale,
				})
				if err != nil {
					t.Fatal(err)
				}
				add(fmt.Sprintf("gitlab rate=%v stale=%d last=%q", deref(rate), stale, last), r.Headline)
			}
		}
	}

	for _, x := range headlines {
		if want := collector.StatusForScore(x.h.Score); x.h.Status != want {
			t.Errorf("%s: status %q, but score %v reads as %q", x.name, x.h.Status, deref(x.h.Score), want)
		}
		if x.h.Score != nil && (*x.h.Score < 0 || *x.h.Score > 100) {
			t.Errorf("%s: score %d is outside 0–100", x.name, *x.h.Score)
		}
		if x.h.Score == nil && x.h.StateLabel == "" {
			t.Errorf("%s: no score and no reason word", x.name)
		}
	}
}

func TestStatusForScore(t *testing.T) {
	if collector.StatusForScore(nil) != collector.StatusUnknown {
		t.Error("no score must be unknown")
	}
	if collector.StatusForScore(ip(scoring.PassThreshold)) != collector.StatusPass {
		t.Error("80 must pass")
	}
	if collector.StatusForScore(ip(scoring.PassThreshold-1)) != collector.StatusFail {
		t.Error("79 must fail")
	}
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func deref[T any](p *T) any {
	if p == nil {
		return "none"
	}
	return *p
}
