package qaimport

import (
	"slices"
	"testing"
)

func TestStatusMaps(t *testing.T) {
	items := map[string]string{
		"PASSED": "passed", "FAILED": "failed", "BLOCKED": "blocked", "HOLD": "blocked",
		"NOT_APPLICABLE": "skipped", "UNTESTED": "untested", "IN_PROGRESS": "untested", "RETEST": "untested", "": "untested",
	}
	for in, want := range items {
		if got := MapItemStatus(in); got != want {
			t.Errorf("MapItemStatus(%q) = %q; want %q", in, got, want)
		}
	}
	for _, in := range []string{"UNTESTED", "IN_PROGRESS", "RETEST", "bogus"} {
		if _, ok := MapResultStatus(in); ok {
			t.Errorf("MapResultStatus(%q) ok; want not a result", in)
		}
	}
	if s, ok := MapResultStatus("HOLD"); !ok || s != "blocked" {
		t.Errorf("MapResultStatus(HOLD) = %q, %v", s, ok)
	}
	if MapIssueStatus("REOPEN") != "reopened" || MapIssueStatus("??") != "new" {
		t.Error("issue status map wrong")
	}
	if MapSeverity("MAJOR", "SHOW_STOPPER") != "blocker" || MapSeverity("TRIVIAL", "HIGH") != "trivial" || MapSeverity("", "") != "major" {
		t.Error("severity map wrong")
	}
	if MapRole("ADMIN") != "admin" || MapRole("TESTER") != "member" || MapRole("EXTERNAL") != "guest" {
		t.Error("role map wrong")
	}
	if RoleRank("admin") <= RoleRank("member") || RoleRank("member") <= RoleRank("guest") {
		t.Error("role rank wrong")
	}
}

func TestGuessPhase(t *testing.T) {
	cases := map[string]string{
		"UAT Plan SIT":                 "uat",
		"[UAT] S00520-DEV":             "uat",
		"Regresi[Staging] - Februari":  "regression",
		"Regression - June [Sprint 1]": "regression",
		"SIT cycle 2":                  "sit",
		"Visit page":                   "other", // "sit" inside a word does not count
		"Smoke prod":                   "smoke",
		"Sanity check":                 "smoke",
		"Sprint 1.1":                   "other",
	}
	for in, want := range cases {
		if got := GuessPhase(in); got != want {
			t.Errorf("GuessPhase(%q) = %q; want %q", in, got, want)
		}
	}
	if GuessPhase("Sprint 1.1", "Regresi September") != "regression" {
		t.Error("later names should be consulted when earlier ones say nothing")
	}
}

func TestTextHelpers(t *testing.T) {
	if CleanText("  -  ") != "" || CleanText(" keep ") != "keep" {
		t.Error("CleanText")
	}
	if CasePriority("CRITICAL") != nil || *CasePriority("HIGH") != "high" || CasePriority("") != nil {
		t.Error("CasePriority")
	}
	if got := RunNotes("UAT env", "1.2", "Chrome"); got != "UAT env\nBuild: 1.2\nBrowser: Chrome" {
		t.Errorf("RunNotes = %q", got)
	}
	if got := RunNotes("-", "", ""); got != "" {
		t.Errorf("RunNotes empty = %q", got)
	}
	if got := Slugify("S00652-DEV-WO008/LN-CR E2E Notification"); got != "s00652-dev-wo008-ln-cr-e2e-notification" {
		t.Errorf("Slugify = %q", got)
	}
	if Slugify("!!!") != "project" {
		t.Error("Slugify fallback")
	}
	if got := TruncateRunes("ééééé", 3); got != "éé…" {
		t.Errorf("TruncateRunes = %q", got)
	}
	if got := WithLegacyCode("", "TC-LIST-01"); got != "Legacy code: TC-LIST-01" {
		t.Errorf("WithLegacyCode empty = %q", got)
	}
	if got := WithLegacyCode("notes", "D0001"); got != "Legacy code: D0001\n\nnotes" {
		t.Errorf("WithLegacyCode = %q", got)
	}
}

func TestAllocateKeys(t *testing.T) {
	got := AllocateKeys([]string{"TC0001", "TC0001", "TC0003", "", "TC0004", "D0002"})
	want := []int{1, 5, 3, 6, 4, 2}
	if !slices.Equal(got, want) {
		t.Fatalf("AllocateKeys = %v; want %v", got, want)
	}
}
