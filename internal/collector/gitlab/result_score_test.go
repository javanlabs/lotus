package gitlab

import (
	"testing"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/collector"
)

func rate(v float64) *float64 { return &v }

// TestScore_StaleBranchesCostAShareOfTwenty pins the formula: a perfect
// pipeline with 30 of 40 branches stale loses 30/40 × 20 = 15 points and
// lands at 85 — hygiene costs, but no longer halves a green repository
// (the averaged formula read 62 here).
func TestScore_StaleBranchesCostAShareOfTwenty(t *testing.T) {
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
	if r.Headline.Score == nil || *r.Headline.Score != 85 {
		t.Fatalf("Score = %v, want 85 (100 pipeline − 30/40 × 20)", r.Headline.Score)
	}
	if r.Headline.Status != collector.StatusPass {
		t.Fatalf("Status = %q, want pass", r.Headline.Status)
	}
}

// TestScore_MissingPartIsSkippedNotZero: a repo whose branches sub-call
// failed is scored on pipelines alone, with no stale penalty.
func TestScore_MissingPartIsSkippedNotZero(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		LastPipelineStatus:  "success",
		PipelineSuccessRate: rate(0.9),
		Warnings:            []glclient.SubCallFailure{{Step: "branches", Err: "boom"}},
		BranchesTotal:       40, // present but untrusted: the sub-call failed
		BranchesStale:       40,
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 90 {
		t.Fatalf("Score = %v, want 90 (pipeline only; failed branches sub-call skipped)", r.Headline.Score)
	}
}

// TestScore_NoPipelinesMeansNoScore: branch hygiene alone is not a GitLab
// score. A repository with no CI used to score on its branches — 93 for
// camunda-bpm-platform — and that number was averaged into its
// component.
func TestScore_NoPipelinesMeansNoScore(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		BranchesTotal:  4,
		BranchesActive: 3,
		BranchesStale:  1,
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score != nil {
		t.Fatalf("Score = %d, want none without pipelines", *r.Headline.Score)
	}
	if r.Headline.Status != collector.StatusUnknown || r.Headline.StateLabel != "No pipeline data" {
		t.Fatalf("Status, StateLabel = %q, %q; want unknown, No pipeline data", r.Headline.Status, r.Headline.StateLabel)
	}
}

// TestScore_FailedPipelineCapsAt49: a failed last pipeline holds the
// score to 49 however good the rest is.
func TestScore_FailedPipelineCapsAt49(t *testing.T) {
	r, err := buildResult(&glclient.Health{
		LastPipelineStatus:         "failed",
		LastFinishedPipelineStatus: "failed",
		PipelineSuccessRate:        rate(0.95),
		BranchesTotal:              2,
		BranchesActive:             2,
	})
	if err != nil {
		t.Fatalf("buildResult: %v", err)
	}
	if r.Headline.Score == nil || *r.Headline.Score != 49 || r.Headline.Status != collector.StatusFail {
		t.Fatalf("headline = %v %q, want 49 fail", r.Headline.Score, r.Headline.Status)
	}
}

// TestScore_RunningPipelineIsNotAFailure: a pipeline in flight has no
// verdict; the cap reads the newest finished one.
func TestScore_RunningPipelineIsNotAFailure(t *testing.T) {
	for _, tc := range []struct {
		name, finished string
		want           int
	}{
		{"after a success", "success", 90},
		{"after a failure", "failed", 49},
		{"nothing finished yet", "", 90},
	} {
		r, err := buildResult(&glclient.Health{
			LastPipelineStatus:         "running",
			LastFinishedPipelineStatus: tc.finished,
			PipelineSuccessRate:        rate(0.9),
		})
		if err != nil {
			t.Fatalf("%s: buildResult: %v", tc.name, err)
		}
		if r.Headline.Score == nil || *r.Headline.Score != tc.want {
			t.Errorf("%s: Score = %v, want %d", tc.name, r.Headline.Score, tc.want)
		}
	}
}

// A snapshot stored before the newest finished pipeline was recorded
// falls back to the last pipeline's status when that one had finished.
func TestLastFinishedPipelineFallsBackForOldSnapshots(t *testing.T) {
	for in, want := range map[string]string{"failed": "failed", "success": "success", "running": "", "pending": ""} {
		if got := LastFinishedPipeline(&glclient.Health{LastPipelineStatus: in}); got != want {
			t.Errorf("LastFinishedPipeline(last=%q) = %q, want %q", in, got, want)
		}
	}
}
