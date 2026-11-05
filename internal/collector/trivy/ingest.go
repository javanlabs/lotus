package trivy

import (
	"context"
	"time"

	trivyclient "github.com/uyab/lotus/internal/clients/trivy"
	"github.com/uyab/lotus/internal/collector"
)

// ingestor implements the push direction: a customer's own CI runs
// `trivy fs`/`trivy image --format json` and POSTs the raw report to
// /ingest/trivy/:binding_id. Parse reduces it to the exact same
// severityCounts/trivyDetail shape Collect produces for the pull
// direction — task 3.4's own requirement that pull and push differ only
// in Detail.Source.
type ingestor struct{}

func init() {
	collector.RegisterIngestor(collector.KindTrivy, ingestor{})
}

func (ingestor) Kind() collector.Kind { return collector.KindTrivy }

func (ingestor) Parse(ctx context.Context, payload []byte, meta collector.IngestMeta) (collector.Result, error) {
	report, err := trivyclient.ParseFSReport(payload)
	if err != nil {
		return collector.Result{}, collector.ErrConfig(err)
	}

	now := timePtr(nowFunc())
	failOn := FailOnFromSettings(meta.OrgSettings)
	return buildResult(trivyDetail{
		Source:         "push",
		Severity:       report.Severity,
		Counts:         Counts{Total: report.Total, Fixable: report.Fixable, Critical: report.Critical, High: report.High, Medium: report.Medium, Low: report.Low, Unknown: report.Unknown},
		ScanFinishedAt: now,
		RawReport:      trivyclient.CompactForStorage(payload),
	}, failOn)
}

// nowFunc is a var so tests can pin it — every other timestamp in
// trivyDetail comes from upstream data (Harbor's push_time/scan
// end_time); a push report carries neither, so ScanFinishedAt is the one
// field this package itself stamps.
var nowFunc = time.Now
