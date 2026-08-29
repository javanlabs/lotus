package trivy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/clients/harbor"
)

// The Trivy detail page lists CVEs straight out of the stored report, so
// dropping an oversize one left the component with the most findings
// showing none of them. Compact instead.
func TestMarshalVulnReport_CompactsInsteadOfDroppingOversizeReports(t *testing.T) {
	bulky := strings.Repeat("a long vulnerability description. ", 4096)
	report := &harbor.VulnerabilityReport{Severity: "Critical"}
	for i := 0; i < 200; i++ {
		report.Vulnerabilities = append(report.Vulnerabilities, harbor.Vulnerability{
			ID: fmt.Sprintf("CVE-2026-%04d", i), Package: "openssl", Severity: "Critical", Description: bulky,
		})
	}

	stored, err := marshalVulnReport(report)
	if err != nil {
		t.Fatalf("marshalVulnReport: %v", err)
	}
	if stored == nil {
		t.Fatal("oversize report was dropped; the detail page has nothing to list")
	}

	var back harbor.VulnerabilityReport
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("stored report is not valid JSON: %v", err)
	}
	if len(back.Vulnerabilities) != len(report.Vulnerabilities) {
		t.Fatalf("kept %d vulnerabilities, want all %d — the CVE list is the point",
			len(back.Vulnerabilities), len(report.Vulnerabilities))
	}
	if back.Vulnerabilities[0].Description != "" {
		t.Fatal("descriptions survived compaction; they are what made it oversize")
	}
	if back.Vulnerabilities[0].ID != "CVE-2026-0000" {
		t.Fatalf("first CVE id = %q, want it preserved", back.Vulnerabilities[0].ID)
	}
	// The caller's report must not be edited underneath it: Collect uses
	// the same value afterwards to build facts.
	if report.Vulnerabilities[0].Description == "" {
		t.Fatal("compaction mutated the caller's report")
	}
}

func TestMarshalVulnReport_KeepsSmallReportsIntact(t *testing.T) {
	report := &harbor.VulnerabilityReport{
		Severity:        "High",
		Vulnerabilities: []harbor.Vulnerability{{ID: "CVE-2026-1", Description: "short"}},
	}
	stored, err := marshalVulnReport(report)
	if err != nil {
		t.Fatalf("marshalVulnReport: %v", err)
	}
	var back harbor.VulnerabilityReport
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("stored report is not valid JSON: %v", err)
	}
	if back.Vulnerabilities[0].Description != "short" {
		t.Fatalf("description = %q, want it left alone", back.Vulnerabilities[0].Description)
	}
}
