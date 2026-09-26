package repo

import (
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
)

// PassShare counts projects by their Lotus status. A folder reports
// "Passing / Measured passing": an unknown project is left out of the
// share and reported beside it instead, so a project nobody has bound yet
// neither drags a folder down nor, by never being measured, props it up.
type PassShare struct {
	Passing, Failing, Unknown int
}

// Measured is how many projects have a verdict.
func (p PassShare) Measured() int { return p.Passing + p.Failing }

// Percent is the share passing, rounded half up — the same rounding the
// KPI cards use. ok is false when nothing is measured.
func (p PassShare) Percent() (int, bool) {
	m := p.Measured()
	if m == 0 {
		return 0, false
	}
	return int(float64(p.Passing)/float64(m)*100 + 0.5), true
}

func (p *PassShare) add(s collector.Status) {
	switch s {
	case collector.StatusPass:
		p.Passing++
	case collector.StatusFail:
		p.Failing++
	default:
		p.Unknown++
	}
}

// TreeProject is one project as the folder rollup sees it: where it is
// filed, and its already-computed Lotus rollup.
type TreeProject struct {
	ID         uuid.UUID
	Name       string
	FolderID   *uuid.UUID
	Components int
	Rollup     ProjectRollup
}

// FailingProject names a failing project for a folder's row.
type FailingProject struct {
	ID    uuid.UUID
	Name  string
	Score *int
}

// FolderRollup is a folder's aggregate over every project beneath it, at
// any depth.
type FolderRollup struct {
	Projects   int
	Components int
	Share      PassShare
	// Kinds is the same share per integration. A project that does not
	// report a kind is not counted in that kind's share at all.
	Kinds map[string]PassShare
	// Failing lists the failing projects, worst score first and unscored
	// ones last — the order to fix them in.
	Failing []FailingProject
}

// RollupFolders aggregates every folder from the projects beneath it.
//
// The number is deliberately a share of projects passing, not an average
// score. An average lets a healthy project hide a failing one and rewards
// polishing an already-green project as much as fixing a red one; a share
// cannot be offset, moves with every fix, and has one target — all green.
// Every project counts once in every folder above it, so a folder never
// averages another folder's number.
func RollupFolders(folders []Folder, projects []TreeProject) map[uuid.UUID]FolderRollup {
	parents := make(map[uuid.UUID]*uuid.UUID, len(folders))
	acc := make(map[uuid.UUID]*FolderRollup, len(folders))
	for _, f := range folders {
		parents[f.ID] = f.ParentID
		acc[f.ID] = &FolderRollup{Kinds: map[string]PassShare{}}
	}

	for _, p := range projects {
		cur := p.FolderID
		for steps := 0; cur != nil && steps <= len(folders); steps++ {
			r, ok := acc[*cur]
			if !ok {
				break
			}
			r.Projects++
			r.Components += p.Components
			r.Share.add(p.Rollup.Status)
			for kind, k := range p.Rollup.Kinds {
				if k.Count == 0 {
					continue
				}
				ks := r.Kinds[kind]
				ks.add(k.Status)
				r.Kinds[kind] = ks
			}
			if p.Rollup.Status == collector.StatusFail {
				r.Failing = append(r.Failing, FailingProject{ID: p.ID, Name: p.Name, Score: p.Rollup.Score})
			}
			cur = parents[*cur]
		}
	}

	out := make(map[uuid.UUID]FolderRollup, len(acc))
	for id, r := range acc {
		sortFailing(r.Failing)
		out[id] = *r
	}
	return out
}

func sortFailing(fs []FailingProject) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if (a.Score == nil) != (b.Score == nil) {
			return b.Score == nil
		}
		if a.Score != nil && *a.Score != *b.Score {
			return *a.Score < *b.Score
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
