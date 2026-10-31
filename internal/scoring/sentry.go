// Package scoring holds pure, storage-independent scoring algorithms —
// task 3.5 moves the legacy Lotus's Sentry health score here verbatim
// (from /Users/uyab/Code/lotus/web/internal/handlers/sentry_score.go,
// "kode terbaik di repo lama; jangan ditulis ulang" — the best code in
// the old repo, do not rewrite). Only two edits from the original:
//   - SentryIssueSignal now lives in this package instead of being
//     imported from the legacy repo package — scoring has no DB
//     dependency of its own, and never should.
//   - The four Indonesian Detail strings moved into sentryDetailText (an
//     i18n-style lookup map) and were reworded to drop their references
//     to services.yaml, which doesn't exist in this codebase — the SaaS
//     model has metric_bindings instead. mapped is redefined the same
//     way: "this component has a Sentry binding", not "services.yaml
//     declares a sentry_project".
package scoring

import (
	"database/sql"
	"math"
	"time"
)

// SentryIssueSignal is the minimum an issue contributes to a repo's health
// score: how bad its state is, how loud it is, and how recently it fired.
type SentryIssueSignal struct {
	Substatus  string
	EventCount int
	LastSeen   sql.NullTime
}

// Sentry health score — see the original file's own derivation for why
// each factor is shaped the way it is.
//
// One repo's error burden ("load") is the sum over its unresolved issues of
//
//	weight(substatus) × log10(1 + events) × 0.5^(days_since_last_seen / 7)
//
// and the headline score is 100·e^(−load/25), graded A–E. Each factor exists
// for a reason:
//
//   - weight(substatus) — Sentry's own triage states are not equally
//     expensive. An escalating issue is one Sentry's forecast says is hitting
//     more users than before; a regressed one is a fix that did not hold, which
//     costs a re-diagnosis and signals a missing regression test; a new one is
//     untriaged risk; an ongoing one is known and already priced in. Weighting
//     by triage cost is the same idea as severity-weighted defect density.
//
//   - log10(1 + events) — event counts are heavy-tailed: a handful of chatty
//     issues (a 404 on /favicon.ico) produce most events. Summing them linearly
//     lets noise bury a rare crash. Compressing volume logarithmically is why
//     loudness and earthquake magnitude are logarithmic too: what matters is
//     the order of magnitude, not the raw count.
//
//   - 0.5^(days/7) — an issue that stopped firing a week ago is likelier fixed
//     or gone than one firing now. Exponential decay with a 7-day half-life is
//     the standard way to express "relevance fades", and keeps a stale backlog
//     from permanently depressing a repo that has recovered.
//
//   - 100·e^(−load/25) — maps unbounded load onto a bounded, monotonically
//     decreasing 0–100 score with no arbitrary clamp: every extra unit of load
//     costs proportionally less, which matches how a maintainer experiences the
//     difference between 1 and 2 bad issues versus 30 and 31.
//
// Known limitation, stated plainly: without a request-count denominator none of
// this is traffic-normalized, so a busy service scores worse than a quiet one at
// equal reliability. Read a repo against its own history, not its neighbours'.
const (
	// sentryLoadScale is the load at which the score falls to 100/e ≈ 36.8.
	sentryLoadScale = 25.0
	// sentryRecencyHalfLifeDays is how long an issue's contribution takes to
	// halve once it stops firing.
	sentryRecencyHalfLifeDays = 7.0
)

// sentrySubstatusWeight orders Sentry's triage states by what they cost to
// handle. Unknown states fall back to the "ongoing" weight rather than zero, so
// a new Sentry substatus never silently disappears from the score.
var sentrySubstatusWeight = map[string]float64{
	"escalating": 5, // getting worse right now
	"regressed":  4, // a fix that did not hold
	"new":        2, // untriaged, unknown blast radius
	"ongoing":    1, // known and accepted
}

const sentryFallbackWeight = 1.0

// SentryStatusKind classifies a repo's row before any grading happens.
type SentryStatusKind string

const (
	// SentryStatusGraded means the repo reports issues and has a real score.
	SentryStatusGraded SentryStatusKind = "graded"
	// SentryStatusClean means zero issues from a service known to report: it
	// has sent events before, so the silence is a real result.
	SentryStatusClean SentryStatusKind = "clean"
	// SentryStatusNoSignal means zero issues and no sign the service has ever
	// sent anything. There is nothing to grade, so it is neither a pass nor a
	// failure: a green "0 issues" would hide a missing SDK, and a red one
	// would report a defect nobody has seen.
	SentryStatusNoSignal SentryStatusKind = "no_signal"
	// SentryStatusNoData means the binding exists but nothing was ever
	// pulled for it — usually a wrong project slug (the pull 404s) or a
	// sync that has not run yet.
	SentryStatusNoData SentryStatusKind = "no_data"
	// SentryStatusNotMapped means the component has no Sentry binding.
	SentryStatusNotMapped SentryStatusKind = "not_mapped"
)

// SentryStatus is the rendered verdict for one repo.
type SentryStatus struct {
	Kind   SentryStatusKind
	Score  int     // 0–100, only meaningful when Kind is graded
	Load   float64 // raw burden behind the score, for the tooltip
	Grade  string  // A–E, only when graded
	Label  string  // short text for the non-graded states
	Detail string  // tooltip explaining the verdict
	Failed bool    // render red
}

// sentryLoad computes the weighted burden for one repo's issues.
func sentryLoad(signals []SentryIssueSignal, now time.Time) float64 {
	var load float64
	for _, s := range signals {
		weight, ok := sentrySubstatusWeight[s.Substatus]
		if !ok {
			weight = sentryFallbackWeight
		}

		// Floor the count at 1 so an issue always carries some weight even when
		// the event count is missing or zero.
		events := s.EventCount
		if events < 1 {
			events = 1
		}
		volume := math.Log10(1 + float64(events))

		recency := 1.0
		if s.LastSeen.Valid {
			if days := now.Sub(s.LastSeen.Time).Hours() / 24; days > 0 {
				recency = math.Pow(0.5, days/sentryRecencyHalfLifeDays)
			}
		}
		load += weight * volume * recency
	}
	return load
}

// sentryScoreFromLoad maps burden onto 0–100.
func sentryScoreFromLoad(load float64) float64 {
	if load <= 0 {
		return 100
	}
	return 100 * math.Exp(-load/sentryLoadScale)
}

// Grade bands follow the conventional percentage scale used almost everywhere a
// 0–100 score is reduced to a letter: 90 / 80 / 70 / 60. Two reasons to prefer
// it over thresholds tuned to this fleet:
//
//   - It needs no explanation. Everyone reads "A ≥ 90" the same way, whereas a
//     bespoke "A ≥ 85" invites the question of who chose 85 and why.
//   - It is not self-serving. Thresholds calibrated so the current fleet lands
//     on good grades measure the calibration, not the fleet.
//
// The 90 boundary also matches Google Lighthouse, which colours ≥ 90 green,
// 50–89 amber and < 50 red on its own 0–100 scores.
//
// Consequence, stated plainly: this is stricter than a looser 85/70/55/40
// scheme would be, so most repos land roughly one grade lower than that
// alternative would show. That is the point — the yardstick is the common
// one, not one calibrated to flatter the current fleet.
const (
	sentryGradeA = 90.0
	sentryGradeB = 80.0
	sentryGradeC = 70.0
	sentryGradeD = 60.0
)

// sentryGradeLabel gives each band a word, for surfaces where a bare letter and
// a number are more precision than the reader needs — the fleet dashboard wants
// "how worried should I be", not "78 out of 100".
func sentryGradeLabel(grade string) string {
	switch grade {
	case "A":
		return "great"
	case "B":
		return "good"
	case "C":
		return "risk"
	case "D":
		return "poor"
	case "E":
		return "bad"
	default:
		return ""
	}
}

func sentryGrade(score float64) string {
	switch {
	case score >= sentryGradeA:
		return "A"
	case score >= sentryGradeB:
		return "B"
	case score >= sentryGradeC:
		return "C"
	case score >= sentryGradeD:
		return "D"
	default:
		return "E"
	}
}

// sentryDetailText is the i18n-style lookup the four Detail strings below
// were moved into (task 3.5's own instruction) — the originals referenced
// services.yaml, which this codebase has no equivalent of, so the wording
// was updated to describe a metric_bindings row instead.
var sentryDetailText = map[string]string{
	"not_mapped": "This component has no Sentry binding configured — it isn't monitored by Sentry at all.",
	"no_data":    "A Sentry binding exists but no data has been collected yet. Usually the project slug is wrong (the pull 404s) or the first sync hasn't run.",
	"no_signal":  "Sentry has no events from this service, so its health can't be measured. Check that the SDK is installed and tags its events.",
	"clean":      "No unresolved issues, and this service has reported to Sentry before.",
}

// evalSentryStatus turns a component's binding + pulled issues into its row
// verdict. mapped is whether this component has a Sentry binding at all;
// hasSnapshot is whether anything was ever stored for it; sawEvents is whether
// the service is known to have reported, which is what separates "silent
// because nothing is wired up" from "silent because it is genuinely clean".
func evalSentryStatus(mapped, hasSnapshot, sawEvents bool, signals []SentryIssueSignal, now time.Time) SentryStatus {
	switch {
	case !mapped:
		return SentryStatus{
			Kind:   SentryStatusNotMapped,
			Label:  "not set up",
			Detail: sentryDetailText["not_mapped"],
			Failed: true,
		}
	case !hasSnapshot:
		return SentryStatus{
			Kind:   SentryStatusNoData,
			Label:  "not set up",
			Detail: sentryDetailText["no_data"],
			Failed: true,
		}
	case len(signals) == 0:
		if sawEvents {
			return SentryStatus{
				Kind:   SentryStatusClean,
				Score:  100,
				Grade:  sentryGrade(100),
				Label:  "no issues",
				Detail: sentryDetailText["clean"],
			}
		}
		return SentryStatus{
			Kind:   SentryStatusNoSignal,
			Label:  "no signal",
			Detail: sentryDetailText["no_signal"],
		}
	}

	load := sentryLoad(signals, now)
	score := sentryScoreFromLoad(load)
	return SentryStatus{
		Kind:  SentryStatusGraded,
		Score: int(math.Round(score)),
		Load:  load,
		Grade: sentryGrade(score),
	}
}
