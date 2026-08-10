package trivy

import "testing"

// A trimmed but realistic `trivy fs --format json` report for a Flutter repo:
// one pub lockfile result with a HIGH (fixable) and a MEDIUM (no fix), plus a
// result with a null Vulnerabilities array (Trivy emits these for clean targets).
const sampleFSJSON = `{
  "SchemaVersion": 2,
  "CreatedAt": "2026-05-27T10:00:00Z",
  "ArtifactName": "repos/app-mobile",
  "ArtifactType": "filesystem",
  "Results": [
    {
      "Target": "pubspec.lock",
      "Class": "lang-pkgs",
      "Type": "pub",
      "Vulnerabilities": [
        {
          "VulnerabilityID": "CVE-2024-1111",
          "PkgName": "http",
          "InstalledVersion": "0.13.0",
          "FixedVersion": "0.13.5",
          "Severity": "HIGH",
          "Title": "header injection",
          "PrimaryURL": "https://avd.aquasec.com/nvd/cve-2024-1111",
          "CweIDs": ["CWE-93"]
        },
        {
          "VulnerabilityID": "CVE-2023-2222",
          "PkgName": "archive",
          "InstalledVersion": "3.1.0",
          "Severity": "MEDIUM",
          "Title": "zip slip"
        }
      ]
    },
    {
      "Target": "ios/Podfile.lock",
      "Class": "lang-pkgs",
      "Type": "cocoapods",
      "Vulnerabilities": null
    }
  ]
}`

func TestParseFSReport_Counts(t *testing.T) {
	rep, err := ParseFSReport([]byte(sampleFSJSON))
	if err != nil {
		t.Fatalf("ParseFSReport: %v", err)
	}
	if rep.Total != 2 {
		t.Errorf("Total = %d, want 2", rep.Total)
	}
	if rep.High != 1 {
		t.Errorf("High = %d, want 1", rep.High)
	}
	if rep.Medium != 1 {
		t.Errorf("Medium = %d, want 1", rep.Medium)
	}
	if rep.Critical != 0 || rep.Low != 0 || rep.Unknown != 0 {
		t.Errorf("unexpected nonzero bucket: %+v", rep)
	}
	if rep.Fixable != 1 {
		t.Errorf("Fixable = %d, want 1 (only the HIGH has a FixedVersion)", rep.Fixable)
	}
	// Overall severity is the highest present, normalized to Harbor's Title-case.
	if rep.Severity != "High" {
		t.Errorf("Severity = %q, want \"High\"", rep.Severity)
	}
}

func TestParseFSReport_VulnsNormalized(t *testing.T) {
	rep, err := ParseFSReport([]byte(sampleFSJSON))
	if err != nil {
		t.Fatalf("ParseFSReport: %v", err)
	}
	if len(rep.Vulns) != 2 {
		t.Fatalf("len(Vulns) = %d, want 2", len(rep.Vulns))
	}
	v := rep.Vulns[0]
	if v.ID != "CVE-2024-1111" || v.Package != "http" || v.Version != "0.13.0" ||
		v.FixVersion != "0.13.5" || v.Severity != "High" {
		t.Errorf("first vuln not mapped correctly: %+v", v)
	}
}

func TestParseFSReport_Empty(t *testing.T) {
	rep, err := ParseFSReport([]byte(`{"SchemaVersion":2,"Results":[]}`))
	if err != nil {
		t.Fatalf("ParseFSReport: %v", err)
	}
	if rep.Total != 0 || rep.Severity != "" || len(rep.Vulns) != 0 {
		t.Errorf("expected empty report, got %+v", rep)
	}
}

func TestParseFSReport_InvalidJSON(t *testing.T) {
	if _, err := ParseFSReport([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
