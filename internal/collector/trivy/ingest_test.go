package trivy_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
)

func trivyIngestor(t *testing.T) collector.Ingestor {
	t.Helper()
	ing, ok := collector.GetIngestor(collector.KindTrivy)
	if !ok {
		t.Fatal("trivy Ingestor not registered")
	}
	return ing
}

const fsReportFixture = `{"Results":[{"Target":"go.mod","Type":"gomod","Vulnerabilities":[
	{"VulnerabilityID":"CVE-2026-1","PkgName":"golang.org/x/net","InstalledVersion":"0.1.0","FixedVersion":"0.2.0","Severity":"CRITICAL"},
	{"VulnerabilityID":"CVE-2026-2","PkgName":"golang.org/x/text","InstalledVersion":"0.1.0","Severity":"HIGH"}
]}]}`

func TestIngestor_Kind(t *testing.T) {
	if k := trivyIngestor(t).Kind(); k != collector.KindTrivy {
		t.Fatalf("Kind() = %q, want %q", k, collector.KindTrivy)
	}
}

func TestParse_ProducesSameShapeAsPullDiffersOnlyBySource(t *testing.T) {
	ing := trivyIngestor(t)
	result, err := ing.Parse(context.Background(), []byte(fsReportFixture), collector.IngestMeta{
		OrgID: uuid.New(), ComponentID: uuid.New(), BindingID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := collector.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	if result.Headline.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail (1 critical, default fail_on=critical)", result.Headline.Status)
	}
	if result.Headline.CountValue == nil || *result.Headline.CountValue != 2 {
		t.Fatalf("CountValue = %v, want 2", result.Headline.CountValue)
	}
	if result.DetailKind != string(collector.KindTrivy) {
		t.Fatalf("DetailKind = %q, want %q", result.DetailKind, collector.KindTrivy)
	}
}

func TestParse_RespectsOrgSettingsFailOn(t *testing.T) {
	ing := trivyIngestor(t)
	result, err := ing.Parse(context.Background(), []byte(fsReportFixture), collector.IngestMeta{
		OrgID: uuid.New(), ComponentID: uuid.New(), BindingID: uuid.New(),
		OrgSettings: []byte(`{"trivy_fail_on":"high"}`),
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Headline.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail (critical+high both present)", result.Headline.Status)
	}
}

func TestParse_MalformedPayloadReturnsErrConfig(t *testing.T) {
	ing := trivyIngestor(t)
	_, err := ing.Parse(context.Background(), []byte(`not json`), collector.IngestMeta{})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

// TestComputeHeadline_ChangingFailOnChangesStatusWithoutResync is task
// 3.4's own acceptance criterion: an org changing trivy_fail_on from
// critical to any must change a component's displayed status without a
// new Collect/Parse — ComputeHeadline is the pure function task 3.6's
// future re-projection calls again against the same stored counts.
func TestComputeHeadline_ChangingFailOnChangesStatusWithoutResync(t *testing.T) {
	// 2 medium, 0 critical/high — passes under "critical" and "high", but
	// "any" (total>0) must fail it, all from the exact same stored counts.
	counts := trivycollector.Counts{Total: 2, Medium: 2}

	critical := trivycollector.ComputeHeadline(counts, "critical")
	if critical.Status != collector.StatusPass {
		t.Fatalf("fail_on=critical: Status = %q, want pass", critical.Status)
	}
	high := trivycollector.ComputeHeadline(counts, "high")
	if high.Status != collector.StatusFail {
		t.Fatalf("fail_on=high: Status = %q, want fail (medium present, and there is no middle tier)", high.Status)
	}
	any := trivycollector.ComputeHeadline(counts, "any")
	if any.Status != collector.StatusFail {
		t.Fatalf("fail_on=any: Status = %q, want fail (total>0)", any.Status)
	}
	// The policy fails it by capping the score, so the number agrees.
	if any.Score == nil || *any.Score != 79 || critical.Score == nil || *critical.Score != 100 {
		t.Fatalf("scores = %v (any), %v (critical); want 79 and 100", any.Score, critical.Score)
	}
}

// One critical and five high findings score 100 − 20 − 25 = 55, which a
// critical caps at 49: red because the number is.
func TestComputeHeadline_CriticalCapsAt49(t *testing.T) {
	h := trivycollector.ComputeHeadline(trivycollector.Counts{Total: 31, Critical: 1, High: 5}, "critical")
	if h.Score == nil || *h.Score != 49 || h.Status != collector.StatusFail {
		t.Fatalf("headline = %v %q, want 49 fail", h.Score, h.Status)
	}
}
