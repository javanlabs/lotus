package trivy

import (
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

// HeadlineFromSnapshot rebuilds the headline a stored snapshot would get
// from a collect today under the organization's fail-on policy, without
// contacting Harbor: from its counts, or "Not scanned" for a registry
// image with no finished scan and nothing found.
func HeadlineFromSnapshot(snap *repo.TrivySnapshot, failOn string) collector.Headline {
	if snap.Source == "registry" && snap.ScanFinishedAt == nil && snap.Total == 0 {
		return notScannedHeadline()
	}
	return ComputeHeadline(Counts{
		Total: snap.Total, Fixable: snap.Fixable, Critical: snap.Critical, High: snap.High,
		Medium: snap.Medium, Low: snap.Low, Unknown: snap.Unknown,
	}, failOn)
}
