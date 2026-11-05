package sonarqube

import (
	"encoding/json"

	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

// HeadlineFromSnapshot rebuilds the headline a stored snapshot would get
// from a collect today, without contacting SonarQube: the measures come
// from its raw_json (the collect's Detail), or from its columns for a row
// stored without one. ok is false when the snapshot holds nothing to
// score from.
func HeadlineFromSnapshot(snap *repo.SonarSnapshot) (collector.Headline, bool) {
	var m sonar.Measures
	if len(snap.RawJSON) == 0 || json.Unmarshal(snap.RawJSON, &m) != nil {
		m = sonar.Measures{
			Coverage: snap.Coverage, Bugs: snap.Bugs, Vulnerabilities: snap.Vulnerabilities,
			CodeSmells: snap.CodeSmells, SecurityHotspots: snap.SecurityHotspots,
			DuplicatedLinesDensity: snap.DuplicatedLinesDensity, NCLOC: snap.NCLOC, Complexity: snap.Complexity,
			SqaleIndexMin: snap.SqaleIndexMin, TestExecutionTimeMs: snap.TestExecutionTimeMs, LastScan: snap.LastScanAt,
		}
		for dst, src := range map[*string]*string{
			&m.QualityGate: snap.QualityGate, &m.ReliabilityRating: snap.ReliabilityRating,
			&m.SecurityRating: snap.SecurityRating, &m.SqaleRating: snap.SqaleRating, &m.Languages: snap.Languages,
		} {
			if src != nil {
				*dst = *src
			}
		}
	}
	r, err := buildResult(&m)
	if err != nil {
		return collector.Headline{}, false
	}
	return r.Headline, true
}
