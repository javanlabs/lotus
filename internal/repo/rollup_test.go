package repo_test

import (
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func TestRollupStatus_WorstWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []collector.Status
		want collector.Status
	}{
		{"empty", nil, collector.StatusUnknown},
		{"all unknown", []collector.Status{collector.StatusUnknown, collector.StatusUnknown}, collector.StatusUnknown},
		{"pass beats unknown", []collector.Status{collector.StatusUnknown, collector.StatusPass}, collector.StatusPass},
		{"fail beats pass", []collector.Status{collector.StatusPass, collector.StatusFail}, collector.StatusFail},
		{"fail beats everything", []collector.Status{collector.StatusPass, collector.StatusFail, collector.StatusUnknown}, collector.StatusFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repo.RollupStatus(c.in); got != c.want {
				t.Errorf("RollupStatus(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRollupScore_AveragesOnlyScoredEntries(t *testing.T) {
	t.Parallel()
	a, b := 80, 60
	got := repo.RollupScore([]*int{&a, nil, &b})
	if got == nil || *got != 70 {
		t.Fatalf("RollupScore = %v, want 70 (average of 80 and 60, nil excluded)", got)
	}
	c := 79
	if got := repo.RollupScore([]*int{&a, &c}); got == nil || *got != 80 {
		t.Fatalf("RollupScore(80, 79) = %v, want 80 (79.5 rounded half up, never truncated)", got)
	}
	if repo.RollupScore([]*int{nil, nil}) != nil {
		t.Fatal("RollupScore of all-nil must be nil, not 0")
	}
	if repo.RollupScore(nil) != nil {
		t.Fatal("RollupScore of empty must be nil")
	}
}

func TestPassRate_ExcludesUnknownFromDenominator(t *testing.T) {
	t.Parallel()
	rate, ok := repo.PassRate([]collector.Status{
		collector.StatusPass, collector.StatusPass, collector.StatusFail, collector.StatusUnknown, collector.StatusUnknown,
	})
	if !ok {
		t.Fatal("expected ok=true when at least one status is measured")
	}
	// 2 pass out of (2 pass + 0 warn + 1 fail) = 3 measured, unknowns excluded.
	if want := 2.0 / 3.0; rate != want {
		t.Fatalf("PassRate = %v, want %v (unknown excluded from denominator)", rate, want)
	}

	if _, ok := repo.PassRate([]collector.Status{collector.StatusUnknown, collector.StatusUnknown}); ok {
		t.Fatal("expected ok=false when nothing is measured — matches legacy dashboardStats's dims>0 guard")
	}
	if _, ok := repo.PassRate(nil); ok {
		t.Fatal("expected ok=false for an empty set")
	}
}

// A component's score is the average of its scored kinds, rounded once,
// with no cap; its status is read from that score. One red kind does not
// make the component red, but it does make it need attention.
func TestComponentRollup_StatusFromScoreNoCap(t *testing.T) {
	t.Parallel()
	s96, s19, s100, s70 := 96, 19, 100, 70
	kinds := map[string]repo.KindHeadline{
		"sonarqube": {Status: "pass", Score: &s96},
		"trivy":     {Status: "pass", Score: &s100},
		"sentry":    {Status: "fail", Score: &s19},
		"gitlab":    {Status: "unknown"},
	}
	status, score := repo.ComponentRollup(kinds)
	// (96 + 100 + 19) / 3 = 71.67 → 72; GitLab has no score and is skipped.
	if score == nil || *score != 72 || status != collector.StatusFail {
		t.Fatalf("component = %v %q, want 72 fail", score, status)
	}
	if !repo.NeedsAttention(kinds) {
		t.Fatal("a kind at 19 needs attention")
	}
	kinds["sentry"] = repo.KindHeadline{Status: "fail", Score: &s70}
	kinds["sonarqube"] = repo.KindHeadline{Status: "pass", Score: &s100}
	status, score = repo.ComponentRollup(kinds)
	// (100 + 100 + 70) / 3 = 90: green overall, though Sentry is red.
	if score == nil || *score != 90 || status != collector.StatusPass {
		t.Fatalf("component = %v %q, want 90 pass", score, status)
	}
	if !repo.NeedsAttention(kinds) {
		t.Fatal("a green component with a red kind still needs attention")
	}
	if repo.NeedsAttention(map[string]repo.KindHeadline{"gitlab": {Status: "unknown"}}) {
		t.Fatal("a kind with no score does not need attention")
	}
}
