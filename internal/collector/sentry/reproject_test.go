package sentry

import (
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func TestHeadlineFromSnapshots(t *testing.T) {
	i := func(v int) *int { return &v }
	f := func(v float64) *float64 { return &v }
	h, ok := HeadlineFromSnapshots([]repo.SentrySnapshot{
		{Environment: "staging", Score: i(96), Load: f(0.9), TotalIssues: 1},
		{Environment: "production", Score: i(80), Load: f(5.6), TotalIssues: 4},
	})
	if !ok || h.Score == nil || *h.Score != 80 || h.Status != collector.StatusPass || h.Grade != "" {
		t.Fatalf("headline = %+v (want production's 80, healthy, no grade)", h)
	}
	ns, ok := HeadlineFromSnapshots([]repo.SentrySnapshot{{Environment: "all"}})
	if !ok || ns.Score != nil || ns.StateLabel != "No signal" {
		t.Fatalf("no signal = %+v", ns)
	}
	if _, ok := HeadlineFromSnapshots(nil); ok {
		t.Fatal("no environment stored: nothing to rebuild")
	}
}
