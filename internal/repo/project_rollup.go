package repo

import "github.com/uyab/lotus/internal/collector"

// KindRollup is one integration kind's aggregate across every component in
// a project — SonarQube coverage averaged over all of a project's repos,
// say. Count is how many components actually reported this kind, which is
// what the dashboard needs to say "3/17 components reporting".
type KindRollup struct {
	Status collector.Status
	Score  *int
	Count  int
}

// ProjectRollup is a project's single Lotus score plus the per-kind
// breakdown it was built from.
type ProjectRollup struct {
	Status collector.Status
	Score  *int
	Kinds  map[string]KindRollup
}

// RollupProject computes a project's Lotus score by averaging each
// integration kind across the project's components first, and only then
// combining those per-kind averages.
//
// The two-step shape is the point. A flat average over every component
// would weight a kind by how many components happen to report it: a project
// with nine repos wired to GitLab and one SonarQube key would let GitLab
// outvote SonarQube nine to one, and adding a tenth repo would silently
// reweight the project's health without anything about its quality
// changing. Averaging per kind first gives every integration the project
// uses the same say, whatever its component count.
//
// Status is worst-wins at both steps, matching how a single component
// already rolls its own kinds up: one failing integration makes the project
// failing however healthy the rest are. A kind that reports a status but no
// score — Trivy counts findings rather than grading them — therefore shapes
// the status while staying out of the average.
func RollupProject(components []ComponentMetric) ProjectRollup {
	perKindScores := map[string][]*int{}
	perKindStatuses := map[string][]collector.Status{}

	for _, c := range components {
		for kind, kh := range c.Metrics() {
			perKindScores[kind] = append(perKindScores[kind], kh.Score)
			perKindStatuses[kind] = append(perKindStatuses[kind], collector.Status(kh.Status))
		}
	}

	kinds := make(map[string]KindRollup, len(perKindScores))
	kindScores := make([]*int, 0, len(perKindScores))
	kindStatuses := make([]collector.Status, 0, len(perKindScores))

	for kind, scores := range perKindScores {
		k := KindRollup{
			Status: RollupStatus(perKindStatuses[kind]),
			Score:  RollupScore(scores),
			Count:  len(scores),
		}
		kinds[kind] = k
		kindScores = append(kindScores, k.Score)
		kindStatuses = append(kindStatuses, k.Status)
	}

	return ProjectRollup{
		Status: RollupStatus(kindStatuses),
		Score:  RollupScore(kindScores),
		Kinds:  kinds,
	}
}
