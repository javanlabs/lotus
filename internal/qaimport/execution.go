package qaimport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/legacyqa"
)

func dateOnly(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return &d
}

func planName(name, fallback string) string {
	n := TruncateRunes(strings.TrimSpace(name), repo.MaxQANameLength)
	if n == "" {
		return fallback
	}
	return n
}

func (st *projectState) touch(planID uuid.UUID, t time.Time) {
	if t.After(st.lastSeen[planID]) {
		st.lastSeen[planID] = t
	}
}

func (im *Importer) importExecution(ctx context.Context, w *repo.QAImportWriter, legacyProject uuid.UUID, st *projectState, rep *Report) error {
	testPlans, err := im.legacy.TestPlans(ctx, legacyProject)
	if err != nil {
		return err
	}
	releases, err := im.legacy.Releases(ctx, legacyProject)
	if err != nil {
		return err
	}
	runs, err := im.legacy.Runs(ctx, legacyProject)
	if err != nil {
		return err
	}
	details, err := im.legacy.Details(ctx, legacyProject)
	if err != nil {
		return err
	}
	results, err := im.legacy.Results(ctx, legacyProject)
	if err != nil {
		return err
	}

	for _, tp := range testPlans {
		starts, ends := dateOnly(tp.Start), dateOnly(tp.End)
		if starts != nil && ends != nil && ends.Before(*starts) {
			ends = nil
		}
		id, err := w.InsertPlan(repo.QAPlan{
			ProjectID: st.projectID, Name: planName(tp.Name, "Untitled plan"), Phase: GuessPhase(tp.Name),
			StartsOn: starts, EndsOn: ends, CreatedBy: im.user(tp.CreatedBy), CreatedAt: tp.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("plan %q: %w", tp.Name, err)
		}
		st.plans[tp.ID] = id
		st.touch(id, tp.CreatedAt)
		if err := w.Remember("test_plans", tp.ID, id); err != nil {
			return err
		}
		rep.imported("plans", 1)
	}
	runTitlesByRelease := map[uuid.UUID][]string{}
	for _, r := range runs {
		if r.ReleaseID != nil {
			runTitlesByRelease[*r.ReleaseID] = append(runTitlesByRelease[*r.ReleaseID], r.Title)
		}
	}
	for _, rl := range releases {
		starts, ends := dateOnly(rl.Start), dateOnly(rl.End)
		if starts != nil && ends != nil && ends.Before(*starts) {
			ends = nil
		}
		id, err := w.InsertPlan(repo.QAPlan{
			ProjectID: st.projectID, Name: planName(rl.Name, "Untitled release"),
			Phase:    GuessPhase(append([]string{rl.Name}, runTitlesByRelease[rl.ID]...)...),
			StartsOn: starts, EndsOn: ends, CreatedBy: im.user(rl.CreatedBy), CreatedAt: rl.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("release %q: %w", rl.Name, err)
		}
		st.plans[rl.ID] = id
		st.touch(id, rl.CreatedAt)
		if err := w.Remember("releases", rl.ID, id); err != nil {
			return err
		}
		rep.imported("plans", 1)
	}

	cycles := map[uuid.UUID]int{}
	for _, r := range runs {
		planID, err := im.planForRun(w, st, r, rep)
		if err != nil {
			return err
		}
		cycles[planID]++
		name := TruncateRunes(strings.TrimSpace(r.Title), repo.MaxQANameLength)
		if name == "" {
			name = "Untitled run"
		}
		var closed *time.Time
		if r.Archived {
			t := r.CreatedAt
			closed = &t
		}
		id, err := w.InsertRun(repo.QARun{
			PlanID: planID, Name: name, Cycle: cycles[planID], Notes: RunNotes(r.Description, r.Version, r.Browser),
			AssigneeID: im.user(r.AssigneeID), ClosedAt: closed, CreatedBy: im.user(r.CreatedBy), CreatedAt: r.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("run %q: %w", name, err)
		}
		st.runs[r.ID] = id
		st.runPlans[id] = planID
		st.touch(planID, r.CreatedAt)
		if err := w.Remember("test_runs", r.ID, id); err != nil {
			return err
		}
		rep.imported("runs", 1)
	}

	latest := map[uuid.UUID]legacyqa.Result{} // detail -> latest executable result
	for _, r := range results {
		if _, ok := MapResultStatus(r.Status); !ok {
			continue
		}
		if cur, ok := latest[r.DetailID]; !ok || !r.CreatedAt.Before(cur.CreatedAt) {
			latest[r.DetailID] = r
		}
	}
	byRun := map[uuid.UUID][]legacyqa.Detail{}
	for _, d := range details {
		byRun[d.RunID] = append(byRun[d.RunID], d)
	}
	for legacyRun, ds := range byRun {
		runID, ok := st.runs[legacyRun]
		if !ok {
			for range ds {
				rep.skipped("items", "run not imported")
			}
			continue
		}
		sort.SliceStable(ds, func(i, j int) bool {
			if !ds[i].CreatedAt.Equal(ds[j].CreatedAt) {
				return ds[i].CreatedAt.Before(ds[j].CreatedAt)
			}
			return st.cases[ds[i].CaseID].key < st.cases[ds[j].CaseID].key
		})
		for pos, d := range ds {
			c, ok := st.cases[d.CaseID]
			if !ok {
				rep.skipped("items", "case not imported")
				continue
			}
			item := c.snapshot
			item.RunID, item.CaseID, item.Position = runID, c.id, pos+1
			item.GroupName = TruncateRunes(st.paths[c.sectionID], repo.MaxQANameLength)
			item.AssigneeID = im.user(d.AssigneeID)
			item.Status = MapItemStatus(d.Status)
			if lr, ok := latest[d.ID]; ok {
				t := lr.CreatedAt
				item.StatusAt, item.ExecutedBy = &t, im.user(lr.CreatedBy)
			} else {
				item.StatusAt = d.StatusAt
			}
			id, err := w.InsertRunItem(item)
			if err != nil {
				return fmt.Errorf("item %s: %w", d.ID, err)
			}
			st.items[d.ID] = id
			st.itemRuns[id] = runID
			if err := w.Remember("test_run_details", d.ID, id); err != nil {
				return err
			}
			rep.imported("items", 1)
		}
	}

	for _, r := range results {
		itemID, ok := st.items[r.DetailID]
		if !ok {
			rep.skipped("results", "item not imported")
			continue
		}
		status, ok := MapResultStatus(r.Status)
		if !ok {
			rep.skipped("results", "not a result")
			continue
		}
		id, err := w.InsertResult(repo.QAResult{
			ItemID: itemID, Status: status, Comment: CleanText(r.Comment), CreatedBy: im.user(r.CreatedBy), CreatedAt: r.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("result %s: %w", r.ID, err)
		}
		st.results[r.ID] = id
		if planID, ok := st.planOfItem(itemID); ok {
			st.touch(planID, r.CreatedAt)
		}
		if err := w.Remember("results", r.ID, id); err != nil {
			return err
		}
		rep.imported("results", 1)
	}

	if !im.opt.CloseIdleBefore.IsZero() {
		for planID, last := range st.lastSeen {
			if last.Before(im.opt.CloseIdleBefore) {
				if err := w.ClosePlan(planID, last); err != nil {
					return err
				}
				rep.imported("plans_closed", 1)
			}
		}
	}
	return nil
}

func (im *Importer) planForRun(w *repo.QAImportWriter, st *projectState, r legacyqa.Run, rep *Report) (uuid.UUID, error) {
	if r.TestPlanID != nil {
		if id, ok := st.plans[*r.TestPlanID]; ok {
			return id, nil
		}
	}
	if r.ReleaseID != nil {
		if id, ok := st.plans[*r.ReleaseID]; ok {
			return id, nil
		}
	}
	if st.unplanned != nil {
		return *st.unplanned, nil
	}
	id, err := w.InsertPlan(repo.QAPlan{
		ProjectID: st.projectID, Name: "Unplanned (imported)", Phase: "other", CreatedAt: r.CreatedAt,
	})
	if err != nil {
		return uuid.Nil, err
	}
	st.unplanned = &id
	rep.imported("plans", 1)
	return id, nil
}
