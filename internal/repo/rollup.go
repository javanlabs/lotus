package repo

import (
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/scoring"
)

// RollupStatus reduces a set of Statuses to the worst one present: fail >
// pass > unknown. An empty or all-unknown set rolls up to unknown, never
// to a false pass. The score rollups no longer use it — a component's and
// a project's status are read from their own score (collector.
// StatusForScore) — but a set of verdicts still reduces this way.
func RollupStatus(statuses []collector.Status) collector.Status {
	has := make(map[collector.Status]bool, 4)
	for _, s := range statuses {
		has[s] = true
	}
	switch {
	case has[collector.StatusFail]:
		return collector.StatusFail
	case has[collector.StatusPass]:
		return collector.StatusPass
	default:
		return collector.StatusUnknown
	}
}

// RollupScore averages the non-nil scores in the set and rounds once,
// half up; nil if none of them has a score — a kind with nothing to score
// must not silently drag the average toward 0. It used to truncate
// (integer division), so 79.5 read as 79.
func RollupScore(scores []*int) *int {
	return scoring.Rounded(scoring.Average(scores))
}

// PassRate computes the fraction of statuses that are pass, out of every
// status that is pass or fail — unknown is left out of the denominator: a
// component nobody has bound a given kind to shouldn't drag that kind's
// fleet-wide percentage down, since "unmeasured" and "measured and
// failing" are different facts. ok is false when there is nothing to
// measure at all.
func PassRate(statuses []collector.Status) (rate float64, ok bool) {
	pass, denom := 0, 0
	for _, s := range statuses {
		switch s {
		case collector.StatusPass:
			pass++
			denom++
		case collector.StatusFail:
			denom++
		}
	}
	if denom == 0 {
		return 0, false
	}
	return float64(pass) / float64(denom), true
}

// ComponentRollup is a component's own score and status from its kinds:
// the average of the kinds that have a score, rounded once, half up, with
// no cap — a kind's hard failure already lowered that kind's own score,
// and still shows red in its own cell. The status is read from the
// component's score.
func ComponentRollup(kinds map[string]KindHeadline) (collector.Status, *int) {
	scores := make([]*int, 0, len(kinds))
	for _, kh := range kinds {
		scores = append(scores, kh.Score)
	}
	score := RollupScore(scores)
	return collector.StatusForScore(score), score
}

// NeedsAttention is whether any of a component's kinds scores below the
// pass threshold — what "N of M components fail" counts. A
// component's average can be green while one of its kinds is red; this
// is the count that average cannot hide.
func NeedsAttention(kinds map[string]KindHeadline) bool {
	for _, kh := range kinds {
		if kh.Score != nil && !scoring.Passes(*kh.Score) {
			return true
		}
	}
	return false
}
