package gitlab

import (
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func TestHeadlineFromSnapshot(t *testing.T) {
	str := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	// No pipelines stored: no score, whatever the branches say. This is
	// camunda-bpm-platform, which used to read 93.
	none, ok := HeadlineFromSnapshot(&repo.GitlabSnapshot{RawJSON: []byte(`{"BranchesTotal":30,"BranchesActive":27,"BranchesStale":3,"Commits30d":4}`)})
	if !ok || none.Score != nil || none.Status != collector.StatusUnknown || none.StateLabel != "No pipeline data" {
		t.Fatalf("no pipelines = %+v", none)
	}
	cols, ok := HeadlineFromSnapshot(&repo.GitlabSnapshot{PipelineSuccessRate: f(0.85), BranchesTotal: i(10), BranchesActive: i(7), BranchesStale: i(3),
		Commits30d: i(5), LastPipelineStatus: str("failed")})
	if !ok || cols.Score == nil || *cols.Score != 49 || cols.Status != collector.StatusFail {
		t.Fatalf("from columns = %+v", cols)
	}
}
