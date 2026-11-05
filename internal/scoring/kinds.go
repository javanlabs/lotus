package scoring

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Each kind's score, as one pure function of what that kind stores. The
// collectors call these to build a headline, the reproject command calls
// them again over stored snapshots, and each metric page calls them to
// print how its headline came about — so the three can never compute it
// differently.
//
// A hard failure is a cap on the score, never a separate verdict: the
// colour is read from the number (Band), so a capped score is red for the
// reason its breakdown names.

// KindScore is one kind's score on one component.
type KindScore struct {
	// Score is 0–100, nil when there is nothing to score.
	Score *int
	// Reason says why Score is nil (ReasonNoData, ...); "" otherwise.
	Reason string
	// Breakdown is the one-line arithmetic behind Score, for the metric
	// page: "Coverage 90 · Worst rating A → 100 · Gate failed ×0.5 = 48".
	Breakdown string
}

func scored(v int, breakdown string) KindScore {
	return KindScore{Score: &v, Breakdown: breakdown}
}

func unscored(reason string) KindScore {
	return KindScore{Reason: reason}
}

// Caps, each below PassThreshold so a capped score always reads red.
const (
	// TrivyCriticalCap holds a component with any critical finding to 49.
	TrivyCriticalCap = 49
	// TrivyPolicyCap holds a component its organization's trivy_fail_on
	// policy fails to 79.
	TrivyPolicyCap = 79
	// GitLabFailedPipelineCap holds a repository whose last finished
	// pipeline failed to 49.
	GitLabFailedPipelineCap = 49
)

// num writes v with at most one decimal and no trailing zero: 90, 72.5.
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

// --- SonarQube -----------------------------------------------------------

// SonarRatingPoints maps SonarQube's own A–E ratings onto points. They are
// inputs to the score, not Lotus grades.
var SonarRatingPoints = map[string]int{"A": 100, "B": 80, "C": 60, "D": 40, "E": 20}

// SonarInput is what the SonarQube score reads.
type SonarInput struct {
	Coverage    *float64 // percent, nil when not measured
	WorstRating string   // the worst of SonarQube's ratings, "" when none
	QualityGate string   // OK | WARN | ERROR | "" (none)
}

// SonarGateFactor is the quality-gate multiplier (f2a5859): the gate is
// the team's own definition of "good enough to ship", so failing it
// dominates — a failed gate can never score above 50.
func SonarGateFactor(gate string) float64 {
	switch gate {
	case "ERROR":
		return 0.5
	case "WARN":
		return 0.75
	}
	return 1
}

// SonarScore is the average of coverage and the worst rating's points,
// skipping whichever is missing, multiplied by the gate factor and
// rounded once. Neither measured means no score.
func SonarScore(in SonarInput) KindScore {
	var parts []string
	sum, n := 0.0, 0
	if in.Coverage != nil {
		sum += *in.Coverage
		n++
		parts = append(parts, "Coverage "+num(*in.Coverage))
	}
	if pts, ok := SonarRatingPoints[in.WorstRating]; ok {
		sum += float64(pts)
		n++
		parts = append(parts, fmt.Sprintf("Worst rating %s → %d", in.WorstRating, pts))
	}
	if n == 0 {
		return unscored(ReasonNoData)
	}
	factor := SonarGateFactor(in.QualityGate)
	score := RoundHalfUp(sum / float64(n) * factor)
	switch in.QualityGate {
	case "OK":
		parts = append(parts, "Gate passed ×1")
	case "WARN":
		parts = append(parts, "Gate warning ×0.75")
	case "ERROR":
		parts = append(parts, "Gate failed ×0.5")
	}
	return scored(score, fmt.Sprintf("%s = %d", strings.Join(parts, " · "), score))
}

// --- Trivy ---------------------------------------------------------------

// TrivyInput is what the Trivy score reads. PolicyFails is the
// organization's trivy_fail_on verdict on these counts, FailOn its name.
type TrivyInput struct {
	NotScanned     bool
	Critical, High int
	PolicyFails    bool
	FailOn         string
}

// TrivyScore is 100 − 20 per critical − 5 per high, floored at 0; capped
// at TrivyCriticalCap with any critical, otherwise at TrivyPolicyCap when
// the organization's policy fails the image.
func TrivyScore(in TrivyInput) KindScore {
	if in.NotScanned {
		return unscored(ReasonNotScanned)
	}
	raw := max(0, 100-20*in.Critical-5*in.High)
	var b strings.Builder
	if in.Critical == 0 && in.High == 0 {
		b.WriteString("No critical or high findings = 100")
	} else {
		fmt.Fprintf(&b, "100 − %d critical × 20 − %d high × 5 = %d", in.Critical, in.High, raw)
	}
	score := raw
	switch {
	case in.Critical > 0 && raw > TrivyCriticalCap:
		score = TrivyCriticalCap
		fmt.Fprintf(&b, " · a critical finding caps it at %d", TrivyCriticalCap)
	case in.Critical == 0 && in.PolicyFails && raw > TrivyPolicyCap:
		score = TrivyPolicyCap
		policy := in.FailOn
		if policy == "" {
			policy = "critical"
		}
		fmt.Fprintf(&b, " · fails the %q fail-on policy, capped at %d", policy, TrivyPolicyCap)
	}
	if score != raw {
		fmt.Fprintf(&b, " = %d", score)
	}
	return scored(score, b.String())
}

// --- Sentry --------------------------------------------------------------

// SentryScore reads an environment's ported Sentry score (sentry.go, kept
// verbatim): score is what evalSentryStatus returned, already rounded to
// the nearest whole number, nil when there was no signal; load is the
// burden behind it and issues how many unresolved issues it counted. The
// band comes from that rounded score — 79.6 is 80 and healthy.
func SentryScore(score *int, load float64, issues int) KindScore {
	if score == nil {
		return unscored(ReasonNoSignal)
	}
	if issues == 0 {
		return scored(*score, fmt.Sprintf("No unresolved issues = %d", *score))
	}
	return scored(*score, fmt.Sprintf("Load %s from %d issue%s → 100 × e^(−%s/25) = %d",
		num(load), issues, pluralS(issues), num(load), *score))
}

// --- GitLab --------------------------------------------------------------

// GitLabStalePenalty is the most a repository's stale branches can take
// off its score: all of them stale.
const GitLabStalePenalty = 20

// GitLabInput is what the GitLab score reads. LastFinished is the status
// of the default branch's newest finished pipeline: a running or pending
// one has no verdict yet and is not a failure.
type GitLabInput struct {
	SuccessRate                  *float64 // 0–1 over the recent sample, nil with no pipelines
	BranchesTotal, BranchesStale *int     // nil when branches were not read
	LastFinished                 string
}

// GitLabScore is the pipeline success rate in percent minus the share of
// stale branches × GitLabStalePenalty, rounded once, floored at 0, and
// capped at GitLabFailedPipelineCap when the last finished pipeline
// failed. No pipelines means no score: branch hygiene alone is not a
// GitLab score.
func GitLabScore(in GitLabInput) KindScore {
	if in.SuccessRate == nil {
		return unscored(ReasonNoPipelineData)
	}
	rate := *in.SuccessRate * 100
	raw := rate
	parts := []string{"Pipelines " + num(rate) + "%"}
	if in.BranchesTotal != nil && in.BranchesStale != nil && *in.BranchesTotal > 0 {
		penalty := float64(*in.BranchesStale) / float64(*in.BranchesTotal) * GitLabStalePenalty
		raw -= penalty
		if *in.BranchesStale == 0 {
			parts = append(parts, "no stale branches")
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d branches stale −%s", *in.BranchesStale, *in.BranchesTotal, num(penalty)))
		}
	}
	score := max(0, RoundHalfUp(raw))
	breakdown := fmt.Sprintf("%s = %d", strings.Join(parts, " · "), score)
	if in.LastFinished == "failed" && score > GitLabFailedPipelineCap {
		score = GitLabFailedPipelineCap
		breakdown += fmt.Sprintf(" · last finished pipeline failed, capped at %d = %d", GitLabFailedPipelineCap, score)
	}
	return scored(score, breakdown)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
