package scoring

import "testing"

func checkScore(t *testing.T, name string, got KindScore, want *int, breakdown string) {
	t.Helper()
	switch {
	case want == nil && got.Score != nil:
		t.Errorf("%s: score = %d, want none", name, *got.Score)
	case want != nil && got.Score == nil:
		t.Errorf("%s: no score, want %d", name, *want)
	case want != nil && *got.Score != *want:
		t.Errorf("%s: score = %d, want %d", name, *got.Score, *want)
	}
	if breakdown != "" && got.Breakdown != breakdown {
		t.Errorf("%s: breakdown = %q, want %q", name, got.Breakdown, breakdown)
	}
}

func TestSonarScore(t *testing.T) {
	checkScore(t, "failed gate", SonarScore(SonarInput{Coverage: fp(90), WorstRating: "A", QualityGate: "ERROR"}),
		ip(48), "Coverage 90 · Worst rating A → 100 · Gate failed ×0.5 = 48")
	// Truncating twice used to read 89.6% coverage with a passed gate as
	// int((int(89.6)+100)/2) = 94; rounded once it is 95.
	checkScore(t, "rounded once", SonarScore(SonarInput{Coverage: fp(89.6), WorstRating: "A", QualityGate: "OK"}),
		ip(95), "Coverage 89.6 · Worst rating A → 100 · Gate passed ×1 = 95")
	checkScore(t, "warn", SonarScore(SonarInput{Coverage: fp(80), WorstRating: "B", QualityGate: "WARN"}), ip(60), "")
	checkScore(t, "coverage only", SonarScore(SonarInput{Coverage: fp(72.5)}), ip(73), "Coverage 72.5 = 73")
	checkScore(t, "rating only", SonarScore(SonarInput{WorstRating: "C", QualityGate: "OK"}), ip(60), "")
	got := SonarScore(SonarInput{QualityGate: "ERROR"})
	checkScore(t, "nothing measured", got, nil, "")
	if got.Reason != ReasonNoData {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestTrivyScore(t *testing.T) {
	checkScore(t, "clean", TrivyScore(TrivyInput{}), ip(100), "No critical or high findings = 100")
	checkScore(t, "critical cap", TrivyScore(TrivyInput{Critical: 1, High: 5, PolicyFails: true}),
		ip(49), "100 − 1 critical × 20 − 5 high × 5 = 55 · a critical finding caps it at 49 = 49")
	checkScore(t, "below the cap already", TrivyScore(TrivyInput{Critical: 3, PolicyFails: true}), ip(40), "100 − 3 critical × 20 − 0 high × 5 = 40")
	checkScore(t, "policy cap", TrivyScore(TrivyInput{High: 1, PolicyFails: true, FailOn: "critical"}),
		ip(79), `100 − 0 critical × 20 − 1 high × 5 = 95 · fails the "critical" fail-on policy, capped at 79 = 79`)
	checkScore(t, "policy passes", TrivyScore(TrivyInput{High: 2}), ip(90), "")
	checkScore(t, "floor", TrivyScore(TrivyInput{Critical: 9}), ip(0), "")
	got := TrivyScore(TrivyInput{NotScanned: true})
	checkScore(t, "not scanned", got, nil, "")
	if got.Reason != ReasonNotScanned {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestSentryScore(t *testing.T) {
	checkScore(t, "graded", SentryScore(ip(80), 5.6, 4), ip(80), "Load 5.6 from 4 issues → 100 × e^(−5.6/25) = 80")
	checkScore(t, "clean", SentryScore(ip(100), 0, 0), ip(100), "No unresolved issues = 100")
	got := SentryScore(nil, 0, 0)
	checkScore(t, "no signal", got, nil, "")
	if got.Reason != ReasonNoSignal {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestGitLabScore(t *testing.T) {
	checkScore(t, "stale share", GitLabScore(GitLabInput{SuccessRate: fp(0.85), BranchesTotal: ip(10), BranchesStale: ip(3), LastFinished: "success"}),
		ip(79), "Pipelines 85% · 3 of 10 branches stale −6 = 79")
	checkScore(t, "failed cap", GitLabScore(GitLabInput{SuccessRate: fp(0.9), BranchesTotal: ip(4), BranchesStale: ip(0), LastFinished: "failed"}),
		ip(49), "Pipelines 90% · no stale branches = 90 · last finished pipeline failed, capped at 49 = 49")
	checkScore(t, "no branch data", GitLabScore(GitLabInput{SuccessRate: fp(1)}), ip(100), "Pipelines 100% = 100")
	checkScore(t, "running is not a failure", GitLabScore(GitLabInput{SuccessRate: fp(0.95), LastFinished: "success"}), ip(95), "")
	got := GitLabScore(GitLabInput{BranchesTotal: ip(10), BranchesStale: ip(0)})
	checkScore(t, "no pipelines", got, nil, "")
	if got.Reason != ReasonNoPipelineData {
		t.Errorf("reason = %q", got.Reason)
	}
}
