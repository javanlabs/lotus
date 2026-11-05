package trivy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/clients/harbor"
	trivyclient "github.com/uyab/lotus/internal/clients/trivy"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
	"github.com/uyab/lotus/internal/tenant"
)

// Counts is the one shape both directions reduce to: Harbor's ScanSummary
// (pull) and trivyclient.FSReport (push) each already carry these same
// six numbers, just computed by different code. Everything downstream of
// this point — Facts, Headline, the persisted row — is identical for
// both, which is task 3.4's own point: "Pull dari Harbor dan push dari CI
// ngasilin bentuk row yang sama, dibedain cuma sama source." Exported so
// task 3.6's future dashboard re-projection can rebuild it straight from
// a stored trivy_snapshots row's own columns and call ComputeHeadline
// again, the same reason ComputeHeadline itself is exported.
type Counts struct {
	Total, Fixable, Critical, High, Medium, Low, Unknown int
}

// trivyDetail is what gets marshaled into Result.Detail and, on the
// persistence path, unmarshaled straight into a trivy_snapshots row.
type trivyDetail struct {
	Source         string // "registry" | "push" | "repo"
	Digest         string
	Tags           []string
	Severity       string
	Counts         Counts
	NotScanned     bool
	Pulls          *int
	ArtifactCount  *int
	ImagePushedAt  *time.Time
	ScanFinishedAt *time.Time
	RawReport      json.RawMessage
}

// FailOnFromSettings reads organizations.settings->>'trivy_fail_on',
// defaulting to "critical" — task 3.4's literal headline table
// ("critical > 0 → fail; high > 0 → warn") IS the critical policy; "high"
// and "any" are progressively stricter opt-ins, never the unannounced
// default.
func FailOnFromSettings(orgSettings json.RawMessage) string {
	var s struct {
		TrivyFailOn string `json:"trivy_fail_on"`
	}
	_ = json.Unmarshal(orgSettings, &s)
	if s.TrivyFailOn == "" {
		return "critical"
	}
	return s.TrivyFailOn
}

// ComputeHeadline is deliberately exported and takes failOn as a plain
// argument, not something it looks up itself — task 3.4's acceptance
// criterion ("fail_on organization diubah dari critical ke any → status
// headline ikut berubah tanpa sync ulang") requires recomputing a
// headline from already-stored counts under a newly-changed policy,
// without a new Collect. Whatever eventually re-projects a stale
// component_metrics_current row on a settings change (task 3.6) calls
// this exact function again with the same stored counts and the new
// failOn.
//
// The policy no longer decides the status on its own: it caps the score
// (scoring.TrivyScore), and the status is read from the score, so the
// number and the colour always agree. A critical finding caps at 49, a
// policy failure without one at 79.
func ComputeHeadline(c Counts, failOn string) collector.Headline {
	ks := scoring.TrivyScore(ScoreInput(c, failOn))
	total := int64(c.Total)
	return collector.Headline{
		Status: collector.StatusForScore(ks.Score), Score: ks.Score, CountValue: &total, CountLabel: "finding",
		Summary: fmt.Sprintf("%d critical, %d high, %d total", c.Critical, c.High, c.Total),
	}
}

// ScoreInput is what scoring.TrivyScore reads from scanned counts under
// the organization's fail-on policy.
func ScoreInput(c Counts, failOn string) scoring.TrivyInput {
	if failOn == "" {
		failOn = "critical"
	}
	return scoring.TrivyInput{Critical: c.Critical, High: c.High, PolicyFails: policyFails(c, failOn), FailOn: failOn}
}

// policyFails is whether the organization's trivy_fail_on policy fails
// these counts: "critical" (the default) on any critical or high finding,
// "high" on medium too, "any" on any finding at all.
func policyFails(c Counts, failOn string) bool {
	switch failOn {
	case "any":
		return c.Total > 0
	case "high":
		return c.Critical > 0 || c.High > 0 || c.Medium > 0
	default: // "critical"
		return c.Critical > 0 || c.High > 0
	}
}

// notScannedHeadline is an image with no finished scan: nothing measured,
// so no score and no status.
func notScannedHeadline() collector.Headline {
	return collector.Headline{
		Status: collector.StatusUnknown, StateLabel: scoring.ReasonNotScanned,
		Summary: "This image has no finished scan in Harbor, so its vulnerabilities are unknown.",
	}
}

func buildResult(d trivyDetail, failOn string) (collector.Result, error) {
	if d.NotScanned {
		detail, err := json.Marshal(d)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}
		return collector.Result{
			Headline: notScannedHeadline(),
			Detail:   detail, DetailKind: string(collector.KindTrivy),
		}, nil
	}
	var facts []collector.Fact
	numFact := func(key string, v int) {
		vv := float64(v)
		facts = append(facts, collector.Fact{Key: key, ValueNum: &vv, Unit: "count"})
	}
	numFact("trivy.total", d.Counts.Total)
	numFact("trivy.critical", d.Counts.Critical)
	numFact("trivy.high", d.Counts.High)
	numFact("trivy.medium", d.Counts.Medium)
	numFact("trivy.low", d.Counts.Low)
	numFact("trivy.fixable", d.Counts.Fixable)

	detail, err := json.Marshal(d)
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}

	headline := ComputeHeadline(d.Counts, failOn)
	return collector.Result{
		Headline:   headline,
		Facts:      facts,
		Detail:     detail,
		DetailKind: string(collector.KindTrivy),
	}, nil
}

// maxVulnReportJSON is the pull path's counterpart to
// trivyclient.MaxRawJSON, and marshalVulnReport compacts the same way
// trivyclient.CompactForStorage does for the push path.
//
// Dropping the report outright used to be the behaviour, on the argument
// that the severity counts live in Facts and the headline anyway. That
// was true of the counts and wrong about the page: the Trivy detail
// screen lists CVEs straight out of this blob, so a large report meant a
// component with the most findings showed none of them.
//
// So: shrink before giving up. Descriptions are the bulk of a Harbor
// report and the one part the detail view does not show, so they go
// first. If it is still over the cap, store it anyway — valid and large
// beats absent, and nothing downstream parses it eagerly.
const maxVulnReportJSON = trivyclient.MaxRawJSON

func marshalVulnReport(vr *harbor.VulnerabilityReport) ([]byte, error) {
	b, err := json.Marshal(vr)
	if err != nil {
		return nil, err
	}
	if len(b) <= maxVulnReportJSON {
		return b, nil
	}

	compact := *vr
	compact.Vulnerabilities = make([]harbor.Vulnerability, len(vr.Vulnerabilities))
	copy(compact.Vulnerabilities, vr.Vulnerabilities)
	for i := range compact.Vulnerabilities {
		compact.Vulnerabilities[i].Description = ""
	}
	return json.Marshal(compact)
}

// Persist writes a Collect's (or Ingestor.Parse's) Detail into
// trivy_snapshots.
func Persist(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, r collector.Result) error {
	var d trivyDetail
	if err := json.Unmarshal(r.Detail, &d); err != nil {
		return err
	}
	_, _, err := repo.InsertTrivySnapshot(ctx, s, repo.NewTrivySnapshot{
		BindingID: bindingID, ComponentID: componentID,
		Source: d.Source, Digest: strPtr(d.Digest), Tags: d.Tags, Severity: strPtr(d.Severity),
		Total: d.Counts.Total, Fixable: d.Counts.Fixable, Critical: d.Counts.Critical,
		High: d.Counts.High, Medium: d.Counts.Medium, Low: d.Counts.Low, Unknown: d.Counts.Unknown,
		Pulls: d.Pulls, ArtifactCount: d.ArtifactCount,
		ImagePushedAt: d.ImagePushedAt, ScanFinishedAt: d.ScanFinishedAt,
		RawJSON: d.RawReport,
	}, collector.ContentHash(r), r.Headline)
	return err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ImportedSnapshot is what a legacy export carries per image: aggregate
// severity counts and scan metadata, but no raw report — the old
// deployment stored Harbor's scan summary, not Trivy's findings.
type ImportedSnapshot struct {
	Digest         string
	Tags           []string
	Severity       string
	Counts         Counts
	Pulls          *int
	ArtifactCount  *int
	ImagePushedAt  *time.Time
	ScanFinishedAt *time.Time
}

// BuildImportedResult turns an imported snapshot into the same
// collector.Result a live registry pull produces, so an import goes
// through Persist and the facts writer exactly like collected data —
// one write path, one set of invariants. Source is "registry" because
// that is what these scans were; the schema's source CHECK knows no
// third value and an import is not a new kind of origin, just an old one.
func BuildImportedResult(in ImportedSnapshot, failOn string) (collector.Result, error) {
	return buildResult(trivyDetail{
		Source: "registry", Digest: in.Digest, Tags: in.Tags, Severity: in.Severity,
		Counts: in.Counts, Pulls: in.Pulls, ArtifactCount: in.ArtifactCount,
		ImagePushedAt: in.ImagePushedAt, ScanFinishedAt: in.ScanFinishedAt,
	}, failOn)
}
