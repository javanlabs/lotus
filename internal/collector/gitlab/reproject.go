package gitlab

import (
	"encoding/json"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

// HeadlineFromSnapshot rebuilds the headline a stored snapshot would get
// from a collect today, without contacting GitLab: the health comes from
// its raw_json (the collect's Detail, warnings and all), or from its
// columns for a row stored without one, a NULL branch count standing for
// a failed branches call. ok is false when the snapshot holds nothing to
// score from.
func HeadlineFromSnapshot(snap *repo.GitlabSnapshot) (collector.Headline, bool) {
	var h glclient.Health
	if len(snap.RawJSON) == 0 || json.Unmarshal(snap.RawJSON, &h) != nil {
		h = glclient.Health{PipelineSuccessRate: snap.PipelineSuccessRate}
		str := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		h.LastPipelineStatus, h.WebURL = str(snap.LastPipelineStatus), str(snap.WebURL)
		if snap.PipelineSampleSize != nil {
			h.PipelineSampleSize = *snap.PipelineSampleSize
		}
		if snap.PipelineSuccessRate == nil {
			h.Warnings = append(h.Warnings, glclient.SubCallFailure{Step: "pipelines", Err: "not stored"})
		}
		if snap.BranchesTotal != nil && snap.BranchesStale != nil {
			h.BranchesTotal, h.BranchesStale = *snap.BranchesTotal, *snap.BranchesStale
			if snap.BranchesActive != nil {
				h.BranchesActive = *snap.BranchesActive
			}
		} else {
			h.Warnings = append(h.Warnings, glclient.SubCallFailure{Step: "branches", Err: "not stored"})
		}
		if snap.Commits30d != nil {
			h.Commits30d = *snap.Commits30d
		} else {
			h.Warnings = append(h.Warnings, glclient.SubCallFailure{Step: "commits", Err: "not stored"})
		}
	}
	r, err := buildResult(&h)
	if err != nil {
		return collector.Headline{}, false
	}
	return r.Headline, true
}
