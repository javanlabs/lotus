package repo_test

import (
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func TestRollupStatus_WorstWins(t *testing.T) {
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
	a, b := 80, 60
	got := repo.RollupScore([]*int{&a, nil, &b})
	if got == nil || *got != 70 {
		t.Fatalf("RollupScore = %v, want 70 (average of 80 and 60, nil excluded)", got)
	}
	if repo.RollupScore([]*int{nil, nil}) != nil {
		t.Fatal("RollupScore of all-nil must be nil, not 0")
	}
	if repo.RollupScore(nil) != nil {
		t.Fatal("RollupScore of empty must be nil")
	}
}

func TestPassRate_ExcludesUnknownFromDenominator(t *testing.T) {
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
