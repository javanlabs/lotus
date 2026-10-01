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

func (im *Importer) importLibrary(ctx context.Context, w *repo.QAImportWriter, legacyProject uuid.UUID, st *projectState, rep *Report) error {
	modules, err := im.legacy.Modules(ctx, legacyProject)
	if err != nil {
		return err
	}
	cases, err := im.legacy.Cases(ctx, legacyProject)
	if err != nil {
		return err
	}
	steps, err := im.legacy.Steps(ctx, legacyProject)
	if err != nil {
		return err
	}

	byID := map[uuid.UUID]legacyqa.Module{}
	for _, m := range modules {
		byID[m.ID] = m
	}
	// A module is kept when it is live, or when a kept case sits in it or
	// in one of its descendants; keeping a module keeps its ancestors.
	keep := map[uuid.UUID]bool{}
	var keepUp func(id uuid.UUID)
	keepUp = func(id uuid.UUID) {
		for i := 0; i < 64; i++ { // bounded: legacy trees may contain cycles
			m, ok := byID[id]
			if !ok || keep[id] {
				return
			}
			keep[id] = true
			if m.ParentID == nil {
				return
			}
			id = *m.ParentID
		}
	}
	for _, m := range modules {
		if m.DeletedAt == nil {
			keepUp(m.ID)
		}
	}
	for _, c := range cases {
		if c.ModuleID != nil {
			keepUp(*c.ModuleID)
		}
	}

	// Insert parents before children: order by depth, then legacy order.
	depth := func(m legacyqa.Module) int {
		d, id := 1, m.ParentID
		for id != nil && d < 64 {
			p, ok := byID[*id]
			if !ok {
				break
			}
			d++
			id = p.ParentID
		}
		return d
	}
	var ordered []legacyqa.Module
	for _, m := range modules {
		if keep[m.ID] {
			ordered = append(ordered, m)
		} else {
			rep.skipped("sections", "deleted and empty")
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return depth(ordered[i]) < depth(ordered[j]) })

	siblingNames := map[string]int{} // parentID|lower(name) -> uses
	newDepth := map[uuid.UUID]int{}
	for _, m := range ordered {
		var parent *uuid.UUID
		d := 1
		if m.ParentID != nil {
			if pid, ok := st.sections[*m.ParentID]; ok {
				parent = &pid
				d = newDepth[pid] + 1
			}
		}
		for d > repo.MaxQASectionDepth && parent != nil {
			// Too deep for the new tree: attach to the grandparent instead.
			rep.warn("section %q flattened to depth %d", m.Name, repo.MaxQASectionDepth)
			parent = st.sectionParents[*parent]
			d--
		}
		name := TruncateRunes(strings.TrimSpace(m.Name), repo.MaxQANameLength)
		if name == "" {
			name = "Untitled"
		}
		parentKey := "root"
		if parent != nil {
			parentKey = parent.String()
		}
		key := parentKey + "|" + strings.ToLower(name)
		siblingNames[key]++
		if n := siblingNames[key]; n > 1 {
			name = TruncateRunes(name, repo.MaxQANameLength-6) + fmt.Sprintf(" (%d)", n)
		}
		var archived *time.Time
		switch {
		case m.DeletedAt != nil:
			archived = m.DeletedAt
		case m.Archived:
			t := m.CreatedAt
			archived = &t
		}
		sid, err := w.InsertSection(repo.QASection{
			ProjectID: st.projectID, ParentID: parent, Name: name, SortOrder: m.SortOrder,
			ArchivedAt: archived, CreatedAt: m.CreatedAt, PicID: im.user(m.PicID),
		})
		if err != nil {
			return fmt.Errorf("section %q: %w", name, err)
		}
		st.sections[m.ID] = sid
		st.sectionParents[sid] = parent
		newDepth[sid] = d
		if parent != nil {
			st.paths[sid] = st.paths[*parent] + " / " + name
		} else {
			st.paths[sid] = name
		}
		if err := w.Remember("modules", m.ID, sid); err != nil {
			return err
		}
		rep.imported("sections", 1)
	}

	codes := make([]string, len(cases))
	for i, c := range cases {
		codes[i] = c.Code
	}
	keys := AllocateKeys(codes)
	for i, c := range cases {
		sectionID, err := im.sectionFor(w, st, c.ModuleID, rep)
		if err != nil {
			return err
		}
		title := TruncateRunes(strings.TrimSpace(c.Title), repo.MaxQATitleLength)
		if title == "" {
			title = "Untitled"
		}
		qsteps := repo.QASteps{}
		for _, s := range steps[c.ID] {
			qsteps = append(qsteps, repo.QAStep{Action: CleanText(s.Action), Expected: CleanText(s.Expected)})
		}
		var archived *time.Time
		switch {
		case c.DeletedAt != nil:
			archived = c.DeletedAt
		case c.Archived:
			t := c.UpdatedAt
			archived = &t
		}
		renumbered := c.Code != "" && keys[i] != keyOf(c.Code)
		notes := CleanText(c.Description)
		if renumbered {
			notes = WithLegacyCode(notes, c.Code)
		}
		qc := repo.QACase{
			ProjectID: st.projectID, SectionID: sectionID, Key: keys[i], Title: title,
			Precondition: CleanText(c.Precondition), Notes: notes, Steps: qsteps,
			Priority: CasePriority(c.Priority), Negative: strings.EqualFold(c.Tag, "NEGATIVE"), Version: 1,
			ArchivedAt: archived, CreatedBy: im.user(c.CreatedBy), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
			PicID: im.user(c.PicID),
		}
		id, err := w.InsertCase(qc)
		if err != nil {
			return fmt.Errorf("case %s %q: %w", c.Code, title, err)
		}
		if err := w.Remember("test_cases", c.ID, id); err != nil {
			return err
		}
		st.cases[c.ID] = importedCase{id: id, key: keys[i], sectionID: sectionID, snapshot: repo.QARunItem{
			Title: qc.Title, Precondition: qc.Precondition, Steps: qc.Steps, CaseVersion: 1,
		}}
		if renumbered {
			rep.warn("case %s %q renumbered to TC-%d", c.Code, title, keys[i])
		}
		rep.imported("cases", 1)
	}
	return nil
}

func keyOf(code string) int { return AllocateKeys([]string{code})[0] }

// sectionFor returns the section a case goes into, creating the
// "Unsorted (imported)" section when its module was not imported.
func (im *Importer) sectionFor(w *repo.QAImportWriter, st *projectState, moduleID *uuid.UUID, rep *Report) (uuid.UUID, error) {
	if moduleID != nil {
		if sid, ok := st.sections[*moduleID]; ok {
			return sid, nil
		}
	}
	if st.unsorted != nil {
		return *st.unsorted, nil
	}
	sid, err := w.InsertSection(repo.QASection{
		ProjectID: st.projectID, Name: "Unsorted (imported)", SortOrder: 1 << 30, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return uuid.Nil, err
	}
	st.unsorted = &sid
	st.paths[sid] = "Unsorted (imported)"
	rep.warn("cases without a module placed in 'Unsorted (imported)'")
	return sid, nil
}
