package sentry

import (
	"testing"
	"time"

	sentryclient "github.com/uyab/lotus/internal/clients/sentry"
	"github.com/uyab/lotus/internal/collector"
)

// A raw score of 79.7 is shown as 80, so it must read as healthy. The
// ported grade cuts at an unrounded 80 and calls it C, which used to paint
// an "80" red; the status now comes from the rounded number.
func TestEnvironmentStatusComesFromTheRoundedScore(t *testing.T) {
	// One new issue (weight 2) with 683 events, firing now: load is
	// 2 × log10(684) ≈ 5.67, and 100 × e^(−5.67/25) ≈ 79.7.
	issues := []sentryclient.Issue{{ID: "1", Substatus: "new", Count: 683}}
	r := buildEnvironmentResult("production", "svc", sentryclient.Project{}, "is:unresolved", time.Now(), issues, true)
	h := r.Headline
	if h.Score == nil || *h.Score != 80 {
		t.Fatalf("score = %v, want 80", h.Score)
	}
	if h.Status != collector.StatusPass {
		t.Fatalf("status = %q, want pass: 80 is healthy", h.Status)
	}
}
