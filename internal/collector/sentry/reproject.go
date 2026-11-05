package sentry

import (
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

// HeadlineFromSnapshots rebuilds a binding's headline from its stored
// environments, without contacting Sentry: each environment's ported
// score as the collect stored it (no signal where it stored none), then
// the worst of them, as Collect picks it. ok is false with no
// environment stored.
func HeadlineFromSnapshots(snaps []repo.SentrySnapshot) (collector.Headline, bool) {
	if len(snaps) == 0 {
		return collector.Headline{}, false
	}
	hs := make([]collector.Headline, 0, len(snaps))
	for _, s := range snaps {
		load := 0.0
		if s.Load != nil {
			load = *s.Load
		}
		hs = append(hs, EnvironmentHeadline(s.Environment, s.Score, load, s.TotalIssues))
	}
	return WorstHeadline(hs), true
}
