package repo

import "github.com/uyab/lotus/internal/collector"

// RollupStatus reduces a set of Statuses to the worst one present —
// task 3.6's rule for rolling a component up from its bindings, and a
// project up from its components: fail > warn > pass > unknown. An empty
// or all-unknown set rolls up to unknown, never to a false pass.
func RollupStatus(statuses []collector.Status) collector.Status {
	has := make(map[collector.Status]bool, 4)
	for _, s := range statuses {
		has[s] = true
	}
	switch {
	case has[collector.StatusFail]:
		return collector.StatusFail
	case has[collector.StatusWarn]:
		return collector.StatusWarn
	case has[collector.StatusPass]:
		return collector.StatusPass
	default:
		return collector.StatusUnknown
	}
}

// RollupScore averages the non-nil scores in the set, or nil if none of
// them have a score — a kind that never emits a Score (Grade-only, or a
// pure pass/fail check) must not silently drag the average toward 0.
func RollupScore(scores []*int) *int {
	sum, n := 0, 0
	for _, s := range scores {
		if s != nil {
			sum += *s
			n++
		}
	}
	if n == 0 {
		return nil
	}
	avg := sum / n
	return &avg
}

// PassRate computes the fraction of statuses that are pass, out of every
// status that is pass, warn, or fail — task 3.6's own instruction to keep
// the legacy dashboardStats behavior of excluding unknown from the
// denominator: a component nobody has bound a given kind to shouldn't
// drag that kind's fleet-wide percentage down, since "unmeasured" and
// "measured and failing" are different facts. ok is false when there is
// nothing to measure at all (every status was unknown, or the set was
// empty), matching dashboardStats's own dims>0 guard.
func PassRate(statuses []collector.Status) (rate float64, ok bool) {
	pass, denom := 0, 0
	for _, s := range statuses {
		switch s {
		case collector.StatusPass:
			pass++
			denom++
		case collector.StatusWarn, collector.StatusFail:
			denom++
		}
	}
	if denom == 0 {
		return 0, false
	}
	return float64(pass) / float64(denom), true
}
