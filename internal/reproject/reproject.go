// Package reproject recomputes every component's headlines and roll-ups
// from the snapshots already stored, without contacting a vendor: the
// `lotus reproject` command. It exists for a change to how scores are
// computed (score bands, caps, rounding) — a deployment can switch every
// row over at once instead of waiting for each binding's next sync, and
// a copy of production that cannot sync at all still shows the new
// numbers.
//
// Each kind's headline is rebuilt by the collector's own code from the
// binding's latest snapshot (HeadlineFromSnapshot), so a reprojected row
// is what a collect would write today. A kind keeps its last-synced time.
// Past days of the public status page are not rewritten; today's is, as
// any collect rewrites it.
package reproject

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	glcollector "github.com/uyab/lotus/internal/collector/gitlab"
	sentrycollector "github.com/uyab/lotus/internal/collector/sentry"
	sqcollector "github.com/uyab/lotus/internal/collector/sonarqube"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// KindChange is one kind's headline on one component, before and after.
type KindChange struct {
	Kind          string
	Before, After repo.KindHeadline
}

// ComponentChange is one component's row, before and after.
type ComponentChange struct {
	ID                        uuid.UUID
	Name                      string
	BeforeStatus, AfterStatus collector.Status
	BeforeScore, AfterScore   *int
	Kinds                     []KindChange
	after                     map[string]repo.KindHeadline
}

// ProjectChange is one project's roll-up, before and after.
type ProjectChange struct {
	Name, Slug    string
	Before, After repo.ProjectRollup
	Components    []ComponentChange
}

// Plan is what a reprojection would write, per project.
type Plan struct {
	Projects []ProjectChange
}

// Build reads an organization's projection rows and rebuilds each kind's
// headline from its binding's latest snapshot. It only reads; failOn is
// the organization's trivy_fail_on policy.
func Build(ctx context.Context, s tenant.Scope, failOn string) (Plan, error) {
	live, err := repo.ListLiveComponents(ctx, s)
	if err != nil {
		return Plan{}, err
	}
	rows, err := repo.ListComponentMetricsForOrg(ctx, s)
	if err != nil {
		return Plan{}, err
	}
	byComponent := make(map[uuid.UUID]repo.ComponentMetric, len(rows))
	for _, r := range rows {
		byComponent[r.ComponentID] = r
	}

	projects := map[uuid.UUID]*ProjectChange{}
	var order []uuid.UUID
	for _, c := range live {
		cm, ok := byComponent[c.ID]
		if !ok {
			continue // never collected: nothing to reproject
		}
		p := projects[c.ProjectID]
		if p == nil {
			p = &ProjectChange{Name: c.ProjectName, Slug: c.ProjectSlug}
			projects[c.ProjectID] = p
			order = append(order, c.ProjectID)
		}
		change, err := buildComponent(ctx, s, c, cm, failOn)
		if err != nil {
			return Plan{}, err
		}
		p.Components = append(p.Components, change)
	}

	var plan Plan
	for _, id := range order {
		p := projects[id]
		var before, after []repo.ComponentMetric
		for _, c := range p.Components {
			cm := byComponent[c.ID]
			before = append(before, cm)
			afterJSON, err := json.Marshal(c.after)
			if err != nil {
				return Plan{}, err
			}
			after = append(after, repo.ComponentMetric{ComponentID: c.ID, MetricsJSON: afterJSON, Status: string(c.AfterStatus), Score: c.AfterScore})
		}
		p.Before, p.After = repo.RollupProject(before), repo.RollupProject(after)
		plan.Projects = append(plan.Projects, *p)
	}
	sort.SliceStable(plan.Projects, func(i, j int) bool {
		return strings.ToLower(plan.Projects[i].Name) < strings.ToLower(plan.Projects[j].Name)
	})
	return plan, nil
}

func buildComponent(ctx context.Context, s tenant.Scope, c repo.LiveComponent, cm repo.ComponentMetric, failOn string) (ComponentChange, error) {
	current := cm.Metrics()
	change := ComponentChange{
		ID: c.ID, Name: c.Name,
		BeforeStatus: cm.StatusValue(), BeforeScore: cm.Score,
		after: make(map[string]repo.KindHeadline, len(current)),
	}
	for k, kh := range current {
		change.after[k] = kh
	}
	bindings, err := repo.ListBindingsForComponent(ctx, s, c.ID, false)
	if err != nil {
		return change, err
	}
	for _, b := range bindings {
		before, ok := current[b.Kind]
		if !ok {
			continue
		}
		h, ok, err := headlineFor(ctx, s, b, failOn)
		if err != nil {
			return change, err
		}
		if !ok {
			continue // nothing stored to rebuild from: keep what is there
		}
		after := repo.KindHeadlineFrom(h)
		after.UpdatedAt = before.UpdatedAt
		change.after[b.Kind] = after
		change.Kinds = append(change.Kinds, KindChange{Kind: b.Kind, Before: before, After: after})
	}
	sort.Slice(change.Kinds, func(i, j int) bool { return kindRank(change.Kinds[i].Kind) < kindRank(change.Kinds[j].Kind) })
	change.AfterStatus, change.AfterScore = repo.ComponentRollup(change.after)
	return change, nil
}

// kindRank is the fixed kind order (AGENTS.md: SonarQube, Trivy, Sentry,
// GitLab), for a readable report.
func kindRank(kind string) int {
	for i, k := range []collector.Kind{collector.KindSonarQube, collector.KindTrivy, collector.KindSentry, collector.KindGitLab} {
		if string(k) == kind {
			return i
		}
	}
	return 99
}

// headlineFor rebuilds one binding's headline from its latest snapshot;
// ok is false when nothing is stored for it.
func headlineFor(ctx context.Context, s tenant.Scope, b repo.Binding, failOn string) (collector.Headline, bool, error) {
	switch collector.Kind(b.Kind) {
	case collector.KindSonarQube:
		snap, err := repo.LatestSonarSnapshot(ctx, s, b.ID)
		if errors.Is(err, repo.ErrNotFound) {
			return collector.Headline{}, false, nil
		} else if err != nil {
			return collector.Headline{}, false, err
		}
		h, ok := sqcollector.HeadlineFromSnapshot(snap)
		return h, ok, nil
	case collector.KindTrivy:
		snap, err := repo.LatestTrivySnapshot(ctx, s, b.ID)
		if errors.Is(err, repo.ErrNotFound) {
			return collector.Headline{}, false, nil
		} else if err != nil {
			return collector.Headline{}, false, err
		}
		return trivycollector.HeadlineFromSnapshot(snap, failOn), true, nil
	case collector.KindGitLab:
		snap, err := repo.LatestGitlabSnapshot(ctx, s, b.ID)
		if errors.Is(err, repo.ErrNotFound) {
			return collector.Headline{}, false, nil
		} else if err != nil {
			return collector.Headline{}, false, err
		}
		h, ok := glcollector.HeadlineFromSnapshot(snap)
		return h, ok, nil
	case collector.KindSentry:
		snaps, err := repo.SentrySnapshotsAt(ctx, s, b.ID, nil)
		if err != nil {
			return collector.Headline{}, false, err
		}
		h, ok := sentrycollector.HeadlineFromSnapshots(snaps)
		return h, ok, nil
	}
	return collector.Headline{}, false, nil
}

// Apply writes a plan: each component's rebuilt kinds, its roll-up, and
// today's status day for its project. Returns how many rows it rewrote.
func Apply(ctx context.Context, s tenant.Scope, plan Plan) (int, error) {
	n := 0
	for _, p := range plan.Projects {
		for _, c := range p.Components {
			kinds := make(map[string]repo.KindHeadline, len(c.Kinds))
			for _, k := range c.Kinds {
				kinds[k.Kind] = k.After
			}
			if len(kinds) == 0 {
				continue
			}
			ok, err := repo.ReprojectComponentMetric(ctx, s, c.ID, kinds)
			if err != nil {
				return n, err
			}
			if ok {
				n++
			}
		}
	}
	return n, nil
}
