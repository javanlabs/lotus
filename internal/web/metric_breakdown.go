package web

import (
	"encoding/json"
	"strconv"

	glclient "github.com/uyab/lotus/internal/clients/gitlab"
	"github.com/uyab/lotus/internal/clients/sonar"
	glcollector "github.com/uyab/lotus/internal/collector/gitlab"
	sqcollector "github.com/uyab/lotus/internal/collector/sonarqube"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
)

// scoreBreakdown is a metric page's one line on how its headline score was
// computed: the score, its band, and the arithmetic behind it —
// "Coverage 90 · Worst rating A → 100 · Gate failed ×0.5 = 48". It is
// built by the same scoring function the collector used for the headline
// (internal/scoring), from the snapshot the page shows, so the line and
// the number in the tab above it are one computation.
type scoreBreakdown struct {
	Score, Tone, Word, Text string
}

func newBreakdown(ks scoring.KindScore) *scoreBreakdown {
	b := scoring.BandOf(ks.Score, ks.Reason)
	bd := &scoreBreakdown{Score: "–", Tone: string(b.Tone), Word: b.Word, Text: ks.Breakdown}
	if ks.Score != nil {
		bd.Score = strconv.Itoa(*ks.Score)
	}
	return bd
}

// sonarBreakdown reads a SonarQube snapshot.
func sonarBreakdown(snap *repo.SonarSnapshot) *scoreBreakdown {
	if snap == nil {
		return nil
	}
	m := sonar.Measures{Coverage: snap.Coverage}
	for dst, src := range map[*string]*string{
		&m.ReliabilityRating: snap.ReliabilityRating, &m.SecurityRating: snap.SecurityRating,
		&m.SqaleRating: snap.SqaleRating, &m.QualityGate: snap.QualityGate,
	} {
		if src != nil {
			*dst = *src
		}
	}
	return newBreakdown(scoring.SonarScore(sqcollector.SonarInput(&m)))
}

// trivyBreakdown reads a Trivy snapshot under the organization's current
// fail-on policy.
func trivyBreakdown(snap *repo.TrivySnapshot, notScanned bool, failOn string) *scoreBreakdown {
	if snap == nil {
		return nil
	}
	if notScanned {
		return newBreakdown(scoring.TrivyScore(scoring.TrivyInput{NotScanned: true}))
	}
	counts := trivycollector.Counts{
		Total: snap.Total, Fixable: snap.Fixable, Critical: snap.Critical, High: snap.High,
		Medium: snap.Medium, Low: snap.Low, Unknown: snap.Unknown,
	}
	return newBreakdown(scoring.TrivyScore(trivycollector.ScoreInput(counts, failOn)))
}

// sentryBreakdown reads one environment's snapshot: the ported score it
// stored, and the load and issue count behind it.
func sentryBreakdown(snap repo.SentrySnapshot) *scoreBreakdown {
	load := 0.0
	if snap.Load != nil {
		load = *snap.Load
	}
	return newBreakdown(scoring.SentryScore(snap.Score, load, snap.TotalIssues))
}

// gitlabHealth is the stored snapshot's raw Health, nil when it has none.
func gitlabHealth(snap *repo.GitlabSnapshot) *glclient.Health {
	if snap == nil || len(snap.RawJSON) == 0 {
		return nil
	}
	var h glclient.Health
	if err := json.Unmarshal(snap.RawJSON, &h); err != nil {
		return nil
	}
	return &h
}

// gitlabLastFinished is the status of the snapshot's newest finished
// pipeline — the one the score's cap reads — or "" with none.
func gitlabLastFinished(snap *repo.GitlabSnapshot) string {
	if h := gitlabHealth(snap); h != nil {
		return glcollector.LastFinishedPipeline(h)
	}
	if snap != nil && snap.LastPipelineStatus != nil && glclient.PipelineFinished(*snap.LastPipelineStatus) {
		return *snap.LastPipelineStatus
	}
	return ""
}

// gitlabBreakdown reads a GitLab snapshot's stored columns (NULL where a
// sub-call failed) and its newest finished pipeline.
func gitlabBreakdown(snap *repo.GitlabSnapshot) *scoreBreakdown {
	if snap == nil {
		return nil
	}
	return newBreakdown(scoring.GitLabScore(scoring.GitLabInput{
		SuccessRate: snap.PipelineSuccessRate, BranchesTotal: snap.BranchesTotal, BranchesStale: snap.BranchesStale,
		LastFinished: gitlabLastFinished(snap),
	}))
}
