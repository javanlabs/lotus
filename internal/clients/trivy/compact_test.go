package trivy

import (
	"encoding/json"
	"testing"
)

func TestCompactReportValidAndSmaller(t *testing.T) {
	// A report with bulky fields (CVSS, References, Layer) that rawReport drops.
	raw := []byte(`{"SchemaVersion":2,"Results":[{"Target":"alpine","Type":"alpine","Vulnerabilities":[
	  {"VulnerabilityID":"CVE-2026-1","PkgName":"openssl","InstalledVersion":"3.5.6","FixedVersion":"3.5.7","Severity":"HIGH","Title":"t","Description":"a long description repeated aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","PrimaryURL":"http://x","CweIDs":["CWE-1"],"CVSS":{"nvd":{"V3Score":7.5,"V3Vector":"AV:N/AC:L"}},"References":["http://a","http://b","http://c"],"Layer":{"DiffID":"sha256:deadbeef"}}
	]}]}`)
	c, err := CompactReport(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(c) {
		t.Fatal("compacted output is not valid JSON")
	}
	if len(c) >= len(raw) {
		t.Fatalf("expected smaller, got %d >= %d", len(c), len(raw))
	}
	rep, err := ParseFSReport(c)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 1 || rep.High != 1 || len(rep.Vulns) != 1 {
		t.Fatalf("bad reparse: %+v", rep)
	}
	if rep.Vulns[0].ID != "CVE-2026-1" || rep.Vulns[0].Description == "" {
		t.Fatalf("lost fields: %+v", rep.Vulns[0])
	}
	// dropDesc strips the description but stays valid + parseable.
	c2, _ := CompactReport(raw, true)
	if !json.Valid(c2) || len(c2) >= len(c) {
		t.Fatalf("dropDesc not smaller/valid: %d vs %d", len(c2), len(c))
	}
	rep2, _ := ParseFSReport(c2)
	if rep2.Vulns[0].Description != "" {
		t.Fatal("description not dropped")
	}
	t.Logf("raw=%d compact=%d nodesc=%d", len(raw), len(c), len(c2))
}

// TestCompactForStorage_HugeReportStillSavesAsValidJSON is task 3.4's own
// acceptance criterion: a Trivy report large enough to need the
// drop-descriptions fallback must still come out as valid, parseable JSON
// — never truncated mid-object.
func TestCompactForStorage_HugeReportStillSavesAsValidJSON(t *testing.T) {
	// Build a report with many vulnerabilities carrying long descriptions,
	// large enough that the no-drop compaction alone exceeds a small test
	// cap, forcing the dropDesc fallback path.
	type vuln struct {
		VulnerabilityID  string `json:"VulnerabilityID"`
		PkgName          string `json:"PkgName"`
		InstalledVersion string `json:"InstalledVersion"`
		Severity         string `json:"Severity"`
		Description      string `json:"Description"`
	}
	longDesc := ""
	for range 200 {
		longDesc += "the quick brown fox jumps over the lazy dog "
	}
	var vulns []vuln
	for i := range 500 {
		vulns = append(vulns, vuln{
			VulnerabilityID: "CVE-2026-" + string(rune('A'+i%26)), PkgName: "pkg", InstalledVersion: "1.0",
			Severity: "HIGH", Description: longDesc,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"Results": []map[string]any{{"Target": "big", "Type": "lang", "Vulnerabilities": vulns}},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	got := CompactForStorage(raw)
	if got == nil {
		t.Fatal("expected a non-nil compacted result")
	}
	if !json.Valid(got) {
		t.Fatal("CompactForStorage must always return valid JSON, never a truncated blob")
	}
	rep, err := ParseFSReport(got)
	if err != nil {
		t.Fatalf("the compacted-for-storage blob must itself be re-parseable: %v", err)
	}
	if rep.Total != 500 {
		t.Fatalf("Total = %d, want 500 (compaction must not drop findings, only description text)", rep.Total)
	}
}

func TestCompactForStorage_UnparseableInputWithinCapIsStoredRaw(t *testing.T) {
	raw := []byte(`not valid json but small`)
	got := CompactForStorage(raw)
	if string(got) != string(raw) {
		t.Fatalf("expected small unparseable input to be stored as-is, got %q", got)
	}
}
