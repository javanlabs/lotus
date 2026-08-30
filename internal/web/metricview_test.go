package web

import (
	"testing"
	"time"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
)

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool        { return &b }

func TestBuildGitlabDetailView_NilSnapshotIsEmptyState(t *testing.T) {
	v := buildGitlabDetailView(nil)
	if v.HasData {
		t.Fatal("HasData = true for a nil snapshot")
	}
	if v.DefaultBranch != "–" || v.LastDuration != "–" || v.LastActivity != "–" {
		t.Fatalf("nil snapshot should render dash placeholders, got %+v", v)
	}
}

func TestBuildGitlabDetailView_FlattensAndDerivesTone(t *testing.T) {
	finished := time.Now().Add(-2 * time.Hour)
	snap := &repo.GitlabSnapshot{
		PrimaryLanguage: strPtr("Go"), DefaultBranch: strPtr("main"), WebURL: strPtr("https://gitlab.example.com/x"),
		BranchesTotal: intPtr(10), BranchesActive: intPtr(3), BranchesStale: intPtr(7),
		LastPipelineStatus: strPtr("success"), LastPipelineWebURL: strPtr("https://gitlab.example.com/x/-/pipelines/1"),
		LastPipelineDurationSec: intPtr(133), LastPipelineFinishedAt: &finished,
		PipelineSuccessRate: floatPtr(0.9), PipelineSampleSize: intPtr(20),
		LastActivityAt: &finished, LastCommitterName: strPtr("Alice"),
		Commits30d: intPtr(42), Commits30dCapped: boolPtr(false),
		DefaultBranchProtected: boolPtr(true), Visibility: strPtr("private"), Archived: boolPtr(false),
	}
	v := buildGitlabDetailView(snap)
	if !v.HasData {
		t.Fatal("HasData = false for a real snapshot")
	}
	if v.Language != "Go" || v.DefaultBranch != "main" {
		t.Errorf("language/branch = %q/%q", v.Language, v.DefaultBranch)
	}
	if v.PipelineTone != "pass" {
		t.Errorf("PipelineTone = %q, want pass for a success pipeline", v.PipelineTone)
	}
	if v.LastDuration != "2m 13s" {
		t.Errorf("LastDuration = %q, want 2m 13s", v.LastDuration)
	}
	if v.SuccessRate != "90% (18/20)" {
		t.Errorf("SuccessRate = %q, want 90%% (18/20)", v.SuccessRate)
	}
	if v.Commits30d != "42" {
		t.Errorf("Commits30d = %q, want 42", v.Commits30d)
	}
	if !v.Protected || v.Archived {
		t.Errorf("Protected/Archived = %v/%v, want true/false", v.Protected, v.Archived)
	}
}

func TestBuildGitlabDetailView_PipelineToneForFailedAndRunning(t *testing.T) {
	cases := map[string]string{"success": "pass", "failed": "fail", "running": "none", "pending": "none", "canceled": "none"}
	for status, wantTone := range cases {
		snap := &repo.GitlabSnapshot{LastPipelineStatus: strPtr(status)}
		v := buildGitlabDetailView(snap)
		if v.PipelineTone != wantTone {
			t.Errorf("status %q: PipelineTone = %q, want %q", status, v.PipelineTone, wantTone)
		}
	}
}

func TestBuildGitlabDetailView_Commits30dCappedShowsPlus(t *testing.T) {
	snap := &repo.GitlabSnapshot{Commits30d: intPtr(100), Commits30dCapped: boolPtr(true)}
	v := buildGitlabDetailView(snap)
	if v.Commits30d != "100+" {
		t.Errorf("Commits30d = %q, want 100+", v.Commits30d)
	}
}

func TestFmtDuration(t *testing.T) {
	cases := map[int]string{0: "–", -5: "–", 47: "47s", 133: "2m 13s", 3725: "1h 2m"}
	for sec, want := range cases {
		if got := fmtDuration(sec); got != want {
			t.Errorf("fmtDuration(%d) = %q, want %q", sec, got, want)
		}
	}
}

func TestBuildSonarDetailView_NilSnapshotIsEmptyState(t *testing.T) {
	v := buildSonarDetailView(nil)
	if v.HasData {
		t.Fatal("HasData = true for a nil snapshot")
	}
	if v.Coverage != "–" || v.QualityGate != "–" {
		t.Fatalf("nil snapshot should render dash placeholders, got %+v", v)
	}
}

func TestBuildSonarDetailView_FailureThresholds(t *testing.T) {
	snap := &repo.SonarSnapshot{
		QualityGate: strPtr("ERROR"), Coverage: floatPtr(85.5), Bugs: intPtr(2), Vulnerabilities: intPtr(1),
		CodeSmells: intPtr(150), DuplicatedLinesDensity: floatPtr(35.0), SecurityHotspots: intPtr(3),
		NCLOC: intPtr(12000), Complexity: intPtr(500),
		ReliabilityRating: strPtr("C"), SecurityRating: strPtr("B"), SqaleRating: strPtr("A"), Languages: strPtr("Go, TS"),
	}
	v := buildSonarDetailView(snap)
	if !v.HasData {
		t.Fatal("HasData = false for a real snapshot")
	}
	if v.QualityGatePass {
		t.Error("QualityGatePass = true for ERROR gate")
	}
	if !v.CoverageFailed {
		t.Error("CoverageFailed = false for 85.5% (< 90 threshold)")
	}
	if !v.BugsFailed || !v.VulnerabilitiesFailed {
		t.Error("BugsFailed/VulnerabilitiesFailed = false for non-zero counts")
	}
	if !v.CodeSmellsFailed {
		t.Error("CodeSmellsFailed = false for 150 (> 100 threshold)")
	}
	if !v.DuplicationsFailed {
		t.Error("DuplicationsFailed = false for 35% (> 30 threshold)")
	}
}

func TestBuildSonarDetailView_PassingThresholdsAtTheBoundary(t *testing.T) {
	snap := &repo.SonarSnapshot{
		QualityGate: strPtr("OK"), Coverage: floatPtr(90.0), Bugs: intPtr(0), Vulnerabilities: intPtr(0),
		CodeSmells: intPtr(100), DuplicatedLinesDensity: floatPtr(30.0),
	}
	v := buildSonarDetailView(snap)
	if !v.QualityGatePass {
		t.Error("QualityGatePass = false for OK gate")
	}
	if v.CoverageFailed || v.BugsFailed || v.VulnerabilitiesFailed || v.CodeSmellsFailed || v.DuplicationsFailed {
		t.Errorf("thresholds are exclusive (< / >), so exactly-at-boundary values must pass: %+v", v)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int64]string{500: "500ms", 1500: "1.5s", 65000: "1m 5s"}
	for ms, want := range cases {
		if got := humanDuration(ms); got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestSonarHelpers_NilAndEmptyRenderDash(t *testing.T) {
	if got := sonarPct(nil); got != "–" {
		t.Errorf("sonarPct(nil) = %q", got)
	}
	if got := sonarInt(nil); got != "–" {
		t.Errorf("sonarInt(nil) = %q", got)
	}
	if got := sonarStr(nil); got != "–" {
		t.Errorf("sonarStr(nil) = %q", got)
	}
	empty := ""
	if got := sonarStr(&empty); got != "–" {
		t.Errorf("sonarStr(&\"\") = %q, want dash", got)
	}
	if got := sonarPct(floatPtr(12.345)); got != "12.3%" {
		t.Errorf("sonarPct(12.345) = %q, want 12.3%%", got)
	}
}

func TestParseTrivyRawJSON_HarborRegistryShape(t *testing.T) {
	raw := []byte(`{
		"severity": "Critical",
		"vulnerabilities": [
			{"id":"CVE-2024-1111","package":"openssl","version":"1.1","fix_version":"1.2","severity":"Critical","description":"desc","links":["https://example.com/cve-1111"],"cwe_ids":["CWE-1"]},
			{"id":"CVE-2024-2222","package":"curl","version":"7.0","fix_version":"","severity":"Low","description":"desc2","links":[],"cwe_ids":[]}
		]
	}`)
	rows := parseTrivyRawJSON(raw)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].CVE != "CVE-2024-1111" || rows[0].Severity != "Critical" || rows[0].SeverityRank != 4 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].Link != "https://example.com/cve-1111" {
		t.Errorf("row0.Link = %q", rows[0].Link)
	}
	if rows[1].Link != "" {
		t.Errorf("row1.Link = %q, want empty (no links)", rows[1].Link)
	}
}

func TestParseTrivyRawJSON_TrivyCLIPushShape(t *testing.T) {
	raw := []byte(`{
		"Results": [
			{"Target":"app","Type":"go-module","Vulnerabilities":[
				{"VulnerabilityID":"CVE-2024-3333","PkgName":"golang.org/x/net","InstalledVersion":"0.1","FixedVersion":"0.2","Severity":"HIGH","Title":"t","Description":"d","PrimaryURL":"https://x","CweIDs":["CWE-2"]}
			]}
		]
	}`)
	rows := parseTrivyRawJSON(raw)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].CVE != "CVE-2024-3333" || rows[0].Severity != "High" || rows[0].SeverityRank != 3 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].Package != "golang.org/x/net" || rows[0].Link != "https://x" {
		t.Errorf("row0 package/link = %q/%q", rows[0].Package, rows[0].Link)
	}
}

func TestParseTrivyRawJSON_UnrecognizedShapeOrEmptyReturnsNil(t *testing.T) {
	if rows := parseTrivyRawJSON(nil); rows != nil {
		t.Errorf("parseTrivyRawJSON(nil) = %v, want nil", rows)
	}
	if rows := parseTrivyRawJSON([]byte(`{"total": 5, "critical": 1}`)); rows != nil {
		t.Errorf("parseTrivyRawJSON(summary-only) = %v, want nil (only header counts, no per-CVE detail)", rows)
	}
	if rows := parseTrivyRawJSON([]byte(`not json`)); rows != nil {
		t.Errorf("parseTrivyRawJSON(invalid json) = %v, want nil", rows)
	}
}

func TestSeverityRank(t *testing.T) {
	cases := map[string]int{"Critical": 4, "High": 3, "Medium": 2, "Low": 1, "Unknown": 0, "": 0}
	for sev, want := range cases {
		if got := severityRank(sev); got != want {
			t.Errorf("severityRank(%q) = %d, want %d", sev, got, want)
		}
	}
}

func TestSentryStatusOptions_OrdersKnownFirstThenAlphabeticalRest(t *testing.T) {
	counts := map[string]int{"ongoing": 3, "new": 1, "escalating": 2, "zeta-custom": 1, "alpha-custom": 4}
	opts := sentryStatusOptions(counts, "ongoing")
	var order []string
	for _, o := range opts {
		order = append(order, o.Value)
	}
	want := []string{"new", "ongoing", "escalating", "alpha-custom", "zeta-custom"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	for _, o := range opts {
		if o.Value == "ongoing" && !o.Selected {
			t.Error("ongoing should be Selected")
		}
		if o.Value != "ongoing" && o.Selected {
			t.Errorf("%q should not be Selected", o.Value)
		}
	}
}

func TestSentryStatusOptions_OmitsCountsAtZero(t *testing.T) {
	opts := sentryStatusOptions(map[string]int{}, "")
	if len(opts) != 0 {
		t.Errorf("opts = %v, want empty for no issues", opts)
	}
}

func TestSentryGradeTone(t *testing.T) {
	cases := map[string]string{"A": "pass", "B": "pass", "C": "fail", "D": "fail", "E": "fail", "": "fail"}
	for grade, want := range cases {
		if got := sentryGradeTone(grade); got != want {
			t.Errorf("sentryGradeTone(%q) = %q, want %q", grade, got, want)
		}
	}
}

func TestRunToneFor(t *testing.T) {
	cases := map[string]string{"ok": "pass", "partial": "fail", "skipped": "none", "failed": "fail", "queued": "none", "leased": "none", "running": "none"}
	for status, want := range cases {
		if got := runToneFor(status); got != want {
			t.Errorf("runToneFor(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestBuildRunRows_FlattensNullableColumnsAndFormatsDuration(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	errClass := "upstream_5xx"
	errMsg := "gitlab returned 503"
	runs := []global.SyncRun{
		{Status: "failed", Trigger: "scheduled", StartedAt: &started, FinishedAt: &finished, ErrorClass: &errClass, Error: &errMsg, WarningsJSON: []byte(`["rate limited"]`)},
		{Status: "ok", Trigger: "manual"}, // no started/finished — never actually ran
	}
	rows := buildRunRows(runs)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].Tone != "fail" || rows[0].Duration != "1m 30s" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].ErrorClass != errClass || rows[0].Error != errMsg || !rows[0].HasWarnings {
		t.Errorf("row0 error/warnings = %+v", rows[0])
	}
	if rows[1].StartedAt != "–" || rows[1].Duration != "–" || rows[1].HasWarnings {
		t.Errorf("row1 (never started) = %+v", rows[1])
	}
}

func TestBuildRunRows_NullWarningsJSONIsNotTreatedAsPresent(t *testing.T) {
	runs := []global.SyncRun{{Status: "ok", Trigger: "scheduled", WarningsJSON: []byte(`null`)}}
	rows := buildRunRows(runs)
	if rows[0].HasWarnings {
		t.Error(`HasWarnings = true for a literal JSON "null" — the column default, not a real warning list`)
	}
}

func TestRelativeTimeStr(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5 min ago"},
		{now.Add(-3 * time.Hour), "3 hr ago"},
		{now.Add(-49 * time.Hour), "2 days ago"},
	}
	for _, c := range cases {
		if got := relativeTimeStr(c.t); got != c.want {
			t.Errorf("relativeTimeStr(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}
