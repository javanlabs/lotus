package repo_test

import (
	"encoding/json"
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func cm(kinds map[string]repo.KindHeadline) repo.ComponentMetric {
	b, _ := json.Marshal(kinds)
	return repo.ComponentMetric{MetricsJSON: b}
}

func score(n int) *int { return &n }

func kh(status string, s *int) repo.KindHeadline {
	return repo.KindHeadline{Status: status, Score: s}
}

// TestRollupProject_AveragesPerKindBeforeCombining is the whole reason this
// function exists rather than a flat average over every component.
//
// Here nine components report only GitLab at 90, and one reports only
// SonarQube at 30. A flat average would be 84 — SonarQube outvoted nine to
// one purely because more repos happen to report GitLab. Averaging each
// kind first gives (90 + 30) / 2 = 60: every integration a project uses
// weighs the same, so adding a tenth repo does not silently reweight the
// project's health.
func TestRollupProject_AveragesPerKindBeforeCombining(t *testing.T) {
	t.Parallel()
	var comps []repo.ComponentMetric
	for i := 0; i < 9; i++ {
		comps = append(comps, cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(90))}))
	}
	comps = append(comps, cm(map[string]repo.KindHeadline{"sonarqube": kh("fail", score(30))}))

	got := repo.RollupProject(comps)
	if got.Score == nil || *got.Score != 60 {
		t.Fatalf("Score = %v, want 60 (per-kind average, not the flat 84)", got.Score)
	}
	if got.Kinds["gitlab"].Count != 9 || got.Kinds["sonarqube"].Count != 1 {
		t.Fatalf("Counts = gitlab:%d sonarqube:%d, want 9 and 1",
			got.Kinds["gitlab"].Count, got.Kinds["sonarqube"].Count)
	}
}

// A project's status is read from its score: no cap at project level, so
// one failing kind lowers the average rather than overriding it. The
// count of components needing attention is what the average cannot hide.
func TestRollupProject_StatusIsTheBandOfTheScore(t *testing.T) {
	t.Parallel()
	got := repo.RollupProject([]repo.ComponentMetric{
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(95)), "sentry": kh("pass", score(90))}),
		cm(map[string]repo.KindHeadline{"sonarqube": kh("fail", score(70))}),
		cm(map[string]repo.KindHeadline{"trivy": kh("unknown", nil)}),
	})
	// (95 + 90 + 70) / 3 = 85 — Trivy has no score and is left out.
	if got.Score == nil || *got.Score != 85 || got.Status != collector.StatusPass {
		t.Fatalf("rollup = %v %q, want 85 pass", got.Score, got.Status)
	}
	if got.Components != 3 || got.NeedAttention != 1 {
		t.Fatalf("need attention = %d of %d, want 1 of 3", got.NeedAttention, got.Components)
	}
	if k := got.Kinds["sonarqube"]; k.Status != collector.StatusFail || k.Below != 1 || k.Scored != 1 {
		t.Fatalf("sonarqube kind = %+v, want fail, 1 of 1 below 80", k)
	}
	if k := got.Kinds["trivy"]; k.Score != nil || k.Status != collector.StatusUnknown || k.Count != 1 || k.Scored != 0 {
		t.Fatalf("trivy kind = %+v, want no score, unknown, reported once", k)
	}
}

// Nothing is rounded until the end. Per kind: GitLab (80 + 81) / 2 = 80.5
// and SonarQube (78 + 78 + 79) / 3 = 78.33. Rounding each kind first would
// give (81 + 78) / 2 = 79.5, rounded to 80; combining unrounded gives
// 79.42, rounded once to 79.
func TestRollupProject_RoundsOnceAtTheEnd(t *testing.T) {
	t.Parallel()
	got := repo.RollupProject([]repo.ComponentMetric{
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(80)), "sonarqube": kh("fail", score(78))}),
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(81)), "sonarqube": kh("fail", score(78))}),
		cm(map[string]repo.KindHeadline{"sonarqube": kh("fail", score(79))}),
	})
	if got.Score == nil || *got.Score != 79 {
		t.Fatalf("Score = %v, want 79", got.Score)
	}
	if k := got.Kinds["gitlab"]; k.Score == nil || *k.Score != 81 {
		t.Fatalf("gitlab kind = %v, want 81 shown (80.5 rounded half up)", k.Score)
	}
	half := repo.RollupProject([]repo.ComponentMetric{
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(80)), "sonarqube": kh("fail", score(79))}),
	})
	if half.Score == nil || *half.Score != 80 || half.Status != collector.StatusPass {
		t.Fatalf("79.5 = %v %q, want 80 pass (half up, not truncated)", half.Score, half.Status)
	}
}

func TestRollupProject_EmptyProjectHasNoScore(t *testing.T) {
	t.Parallel()
	got := repo.RollupProject(nil)
	if got.Score != nil {
		t.Errorf("Score = %v, want nil for a project with no components", got.Score)
	}
	if got.Status != collector.StatusUnknown {
		t.Errorf("Status = %q, want unknown", got.Status)
	}
}
