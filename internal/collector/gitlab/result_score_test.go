package gitlab

import (
	"testing"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
)

func rate(v float64) *float64 { return &v }

// TestScore_BlendsPipelineWithBranchHygiene pins the two-part score: a
// perfect pipeline no longer hides a branch graveyard. 100% success with
// only 10 of 40 branches active must land at (100+25)/2 = 62, not 100.
func TestScore_BlendsPipelineWithBranchHygiene(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		LastPipelineStatus:  "success",
		PipelineSuccessRate: rate(1.0),
		BranchesTotal:       40,
		BranchesActive:      10,
		BranchesStale:       30,
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 62 {
		t.Fatalf("Score = %v, want 62 ((100 pipeline + 25 hygiene) / 2)", r.Headline.Score)
	}
}

// TestScore_MissingPartIsSkippedNotZero: a repo whose branches sub-call
// failed is scored on pipelines alone — the unmeasured half must not drag
// the score down, matching the skip-not-zero rule every rollup follows.
func TestScore_MissingPartIsSkippedNotZero(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		LastPipelineStatus:  "success",
		PipelineSuccessRate: rate(0.9),
		Warnings:            []glclient.SubCallFailure{{Step: "branches", Err: "boom"}},
		BranchesTotal:       40, // present but untrusted: the sub-call failed
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 90 {
		t.Fatalf("Score = %v, want 90 (pipeline only; failed branches sub-call skipped)", r.Headline.Score)
	}
}

// TestScore_BranchHygieneAloneWhenNoPipelines: a repo with no CI at all
// still gets a hygiene score rather than none.
func TestScore_BranchHygieneAloneWhenNoPipelines(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		BranchesTotal:  4,
		BranchesActive: 3,
		BranchesStale:  1,
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 75 {
		t.Fatalf("Score = %v, want 75 (3 of 4 branches active)", r.Headline.Score)
	}
}
