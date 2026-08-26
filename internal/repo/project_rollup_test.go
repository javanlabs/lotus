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

// A project's status is worst-wins across kinds, matching how a component
// already rolls its own kinds up: one failing integration makes the project
// failing, however healthy the rest are.
func TestRollupProject_StatusIsWorstWins(t *testing.T) {
	got := repo.RollupProject([]repo.ComponentMetric{
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(95))}),
		cm(map[string]repo.KindHeadline{"sonarqube": kh("warn", score(70))}),
		cm(map[string]repo.KindHeadline{"trivy": kh("fail", nil)}),
	})
	if got.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail", got.Status)
	}
}

// A kind that reports a status but no score (Trivy counts findings, it does
// not grade) must still shape the status without dragging the average.
func TestRollupProject_ScorelessKindAffectsStatusNotScore(t *testing.T) {
	got := repo.RollupProject([]repo.ComponentMetric{
		cm(map[string]repo.KindHeadline{"gitlab": kh("pass", score(80))}),
		cm(map[string]repo.KindHeadline{"trivy": kh("warn", nil)}),
	})
	if got.Score == nil || *got.Score != 80 {
		t.Fatalf("Score = %v, want 80 (the scoreless kind must not count)", got.Score)
	}
	if got.Status != collector.StatusWarn {
		t.Fatalf("Status = %q, want warn (the scoreless kind still counts here)", got.Status)
	}
}

func TestRollupProject_EmptyProjectHasNoScore(t *testing.T) {
	got := repo.RollupProject(nil)
	if got.Score != nil {
		t.Errorf("Score = %v, want nil for a project with no components", got.Score)
	}
	if got.Status != collector.StatusUnknown {
		t.Errorf("Status = %q, want unknown", got.Status)
	}
}
