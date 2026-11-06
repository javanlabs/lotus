package repo

import (
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/scoring"
)

// KindRollup is one integration kind's aggregate across every component in
// a project — SonarQube averaged over all of a project's repos, say.
type KindRollup struct {
	// Status is read from Score: pass at 80 or more.
	Status collector.Status
	// Score is Avg rounded half up, for display; nil when no component
	// has a score for the kind.
	Score *int
	// Avg is the unrounded average the project score is built from, so
	// the project rounds once rather than once per kind.
	Avg *float64
	// Count is how many components report this kind at all (scored or
	// not); Scored how many have a score, and Below how many of those
	// score under 80 — the KPI tab's "N of M below 80".
	Count, Scored, Below int
}

// ProjectRollup is a project's single Lotus score plus the per-kind
// breakdown it was built from.
type ProjectRollup struct {
	// Status is read from Score.
	Status collector.Status
	Score  *int
	Kinds  map[string]KindRollup
	// Components is how many components were rolled up, and
	// NeedAttention how many of them have at least one kind below 80 —
	// "N of M components need attention", the count an average hides.
	Components, NeedAttention int
}

// RollupProject computes a project's Lotus score by averaging each
// integration kind across the project's components first, and only then
// combining those per-kind averages (0432478).
//
// The two-step shape is the point. A flat average over every component
// would weight a kind by how many components happen to report it: a project
// with nine repos wired to GitLab and one SonarQube key would let GitLab
// outvote SonarQube nine to one. Averaging per kind first gives every
// integration the project uses the same say, whatever its component count.
// A kind or a component with no score is left out of every average, never
// counted as zero.
//
// Nothing is rounded until the end: the per-kind averages are combined
// unrounded and the project's score is rounded once, half up. If any
// component has failed (score < 80), the project score is capped at the
// lowest component score: a project cannot pass while one of its components
// fails. The status is read from the score, so a project's verdict is
// its band — and NeedAttention says how many components that average is
// hiding.
func RollupProject(components []ComponentMetric) ProjectRollup {
	perKindScores := map[string][]*int{}
	roll := ProjectRollup{Components: len(components)}
	var minCompScore *int

	for _, c := range components {
		kinds := c.Metrics()
		if NeedsAttention(kinds) {
			roll.NeedAttention++
		}
		for kind, kh := range kinds {
			perKindScores[kind] = append(perKindScores[kind], kh.Score)
		}

		compScore := c.Score
		if compScore == nil && len(kinds) > 0 {
			_, compScore = ComponentRollup(kinds)
		}
		if compScore != nil {
			if minCompScore == nil || *compScore < *minCompScore {
				minCompScore = compScore
			}
		}
	}

	roll.Kinds = make(map[string]KindRollup, len(perKindScores))
	kindAvgs := make([]*float64, 0, len(perKindScores))
	for kind, scores := range perKindScores {
		k := KindRollup{Avg: scoring.Average(scores), Count: len(scores)}
		k.Score = scoring.Rounded(k.Avg)
		k.Status = collector.StatusForScore(k.Score)
		for _, s := range scores {
			if s != nil {
				k.Scored++
				if !scoring.Passes(*s) {
					k.Below++
				}
			}
		}
		roll.Kinds[kind] = k
		kindAvgs = append(kindAvgs, k.Avg)
	}

	roll.Score = scoring.Rounded(scoring.AverageFloat(kindAvgs))
	if minCompScore != nil && !scoring.Passes(*minCompScore) && roll.Score != nil {
		capped := scoring.Cap(*roll.Score, *minCompScore)
		roll.Score = &capped
	}
	roll.Status = collector.StatusForScore(roll.Score)
	return roll
}
