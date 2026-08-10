// Package trivy parses the JSON report emitted by `trivy fs`/`trivy image
// --format json`. Ported from the legacy Lotus
// (/Users/uyab/Code/lotus/web/internal/clients/trivy) as-is —
// ParseFSReport, CompactReport, normalizeSeverity, and highestSeverity are
// unchanged. This is task 3.4's ingest parser: a customer's own CI runs
// the trivy CLI and POSTs the JSON here (see internal/collector/trivy's
// Ingestor), rather than Lotus ever needing to reach the customer's
// registry or filesystem itself.
package trivy

import (
	"encoding/json"
	"fmt"
)

// Vulnerability is one CVE entry, normalized to Harbor's Title-case severity so
// the same rendering/rollup logic works for both the pull and push paths.
type Vulnerability struct {
	ID          string
	Package     string
	Version     string
	FixVersion  string
	Severity    string
	Title       string
	Description string
	PrimaryURL  string
	CweIDs      []string
}

// FSReport is the flattened summary of a filesystem or image scan.
type FSReport struct {
	Severity string // highest severity present, Title-case; "" when no findings
	Total    int
	Fixable  int
	Critical int
	High     int
	Medium   int
	Low      int
	Unknown  int
	Vulns    []Vulnerability
}

// rawReport mirrors the subset of `trivy fs`/`trivy image --format json` we consume.
type rawReport struct {
	Results []struct {
		Target          string `json:"Target"`
		Type            string `json:"Type"`
		Vulnerabilities []struct {
			VulnerabilityID  string   `json:"VulnerabilityID"`
			PkgName          string   `json:"PkgName"`
			InstalledVersion string   `json:"InstalledVersion"`
			FixedVersion     string   `json:"FixedVersion"`
			Severity         string   `json:"Severity"`
			Title            string   `json:"Title"`
			Description      string   `json:"Description"`
			PrimaryURL       string   `json:"PrimaryURL"`
			CweIDs           []string `json:"CweIDs"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

func ParseFSReport(data []byte) (FSReport, error) {
	var raw rawReport
	if err := json.Unmarshal(data, &raw); err != nil {
		return FSReport{}, fmt.Errorf("parse trivy fs report: %w", err)
	}

	var out FSReport
	for _, res := range raw.Results {
		for _, v := range res.Vulnerabilities {
			sev := normalizeSeverity(v.Severity)
			out.Vulns = append(out.Vulns, Vulnerability{
				ID:          v.VulnerabilityID,
				Package:     v.PkgName,
				Version:     v.InstalledVersion,
				FixVersion:  v.FixedVersion,
				Severity:    sev,
				Title:       v.Title,
				Description: v.Description,
				PrimaryURL:  v.PrimaryURL,
				CweIDs:      v.CweIDs,
			})
			out.Total++
			if v.FixedVersion != "" {
				out.Fixable++
			}
			switch sev {
			case "Critical":
				out.Critical++
			case "High":
				out.High++
			case "Medium":
				out.Medium++
			case "Low":
				out.Low++
			default:
				out.Unknown++
			}
		}
	}
	out.Severity = highestSeverity(out)
	return out, nil
}

// CompactReport re-serializes a Trivy JSON report down to only the fields
// consumed above (the rawReport subset), producing compact, valid JSON.
//
// The stored blob lets a per-CVE detail view re-parse it later. The full
// `trivy image --format json` output is large (CVSS scores, references,
// layers, SBOM…), so byte-truncating it to a size cap yields invalid JSON
// mid-cut — this re-marshals the parsed subset instead, keeping the blob
// small AND always valid.
//
// When dropDesc is true the (longest) Description field is omitted too, for
// the rare report still oversize after compaction.
func CompactReport(data []byte, dropDesc bool) ([]byte, error) {
	var raw rawReport
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("compact trivy report: %w", err)
	}
	if dropDesc {
		for i := range raw.Results {
			for j := range raw.Results[i].Vulnerabilities {
				raw.Results[i].Vulnerabilities[j].Description = ""
			}
		}
	}
	return json.Marshal(raw)
}

// normalizeSeverity maps Trivy's upper-case severities to Harbor's Title-case
// convention. Anything unrecognized (incl. "UNKNOWN"/"NONE") becomes "Unknown".
func normalizeSeverity(s string) string {
	switch s {
	case "CRITICAL":
		return "Critical"
	case "HIGH":
		return "High"
	case "MEDIUM":
		return "Medium"
	case "LOW":
		return "Low"
	default:
		return "Unknown"
	}
}

func highestSeverity(r FSReport) string {
	switch {
	case r.Critical > 0:
		return "Critical"
	case r.High > 0:
		return "High"
	case r.Medium > 0:
		return "Medium"
	case r.Low > 0:
		return "Low"
	case r.Unknown > 0:
		return "Unknown"
	default:
		return ""
	}
}

// MaxRawJSON caps the stored report so a noisy scan can't bloat the row —
// ported from the legacy jobs/sync_trivy_fs.go's own constant.
const MaxRawJSON = 2_000_000

// CompactForStorage degrades gracefully: compact first, drop descriptions
// if still oversize, then accept an oversize-but-valid blob, and only as a
// last resort (unparseable input) fall back to storing raw bytes or
// skipping entirely — ported from the legacy compactTrivyRaw with the
// same fallback order, generalized to return []byte instead of string.
func CompactForStorage(raw []byte) []byte {
	if c, err := CompactReport(raw, false); err == nil {
		if len(c) <= MaxRawJSON {
			return c
		}
		if c2, err := CompactReport(raw, true); err == nil && len(c2) <= MaxRawJSON {
			return c2
		}
		return c // oversize but valid beats truncated + corrupt
	}
	if len(raw) > MaxRawJSON {
		return nil // can't compact and too big to store whole — skip rather than corrupt
	}
	return raw
}
