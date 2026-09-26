package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// treeIndentPx is how far each folder level indents the rows inside it.
const treeIndentPx = 18

// maxFailingNamed caps how many failing projects a folder row names before
// "+N more".
const maxFailingNamed = 3

// folderOption is one folder as a picker lists it.
type folderOption struct {
	ID    uuid.UUID
	Name  string // the folder's own name
	Path  string // "IT Services / Tribe lead A"
	Depth int    // 1 at the top level
}

func parentKey(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func sortFolders(fs []repo.Folder) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].SortOrder != fs[j].SortOrder {
			return fs[i].SortOrder < fs[j].SortOrder
		}
		return strings.ToLower(fs[i].Name) < strings.ToLower(fs[j].Name)
	})
}

func sortProjects(ps []repo.Project) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].SortOrder != ps[j].SortOrder {
			return ps[i].SortOrder < ps[j].SortOrder
		}
		return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name)
	})
}

func childFolders(folders []repo.Folder) map[uuid.UUID][]repo.Folder {
	children := make(map[uuid.UUID][]repo.Folder, len(folders))
	for _, f := range folders {
		k := parentKey(f.ParentID)
		children[k] = append(children[k], f)
	}
	for k := range children {
		sortFolders(children[k])
	}
	return children
}

// folderOptionsFor lists every folder depth-first, each with its full path.
func folderOptionsFor(folders []repo.Folder) []folderOption {
	children := childFolders(folders)
	out := make([]folderOption, 0, len(folders))
	seen := make(map[uuid.UUID]bool, len(folders))
	var walk func(parent uuid.UUID, path string, depth int)
	walk = func(parent uuid.UUID, path string, depth int) {
		for _, f := range children[parent] {
			if seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			p := f.Name
			if path != "" {
				p = path + " / " + f.Name
			}
			out = append(out, folderOption{ID: f.ID, Name: f.Name, Path: p, Depth: depth})
			walk(f.ID, p, depth+1)
		}
	}
	walk(uuid.Nil, "", 1)
	return out
}

// folderSelect turns folder options into a picker: "Top level" first, then
// every folder as an indented tree (depth-first, so each folder sits under
// its parent), minus any the caller excludes. Each option's Label is the
// full path, which the box shows once chosen.
func folderSelect(opts []folderOption, exclude func(folderOption) bool) []httpx.SelectOption {
	out := make([]httpx.SelectOption, 0, len(opts)+1)
	out = append(out, httpx.SelectOption{Value: "", Label: "Top level"})
	for _, o := range opts {
		if exclude != nil && exclude(o) {
			continue
		}
		out = append(out, httpx.SelectOption{Value: o.ID.String(), Label: o.Path, Name: o.Name, Depth: o.Depth - 1})
	}
	return out
}

// folderSubtree returns a folder and everything beneath it, and how many
// levels that subtree spans (1 for a folder with no subfolders).
func folderSubtree(children map[uuid.UUID][]repo.Folder, id uuid.UUID) (map[uuid.UUID]bool, int) {
	members := map[uuid.UUID]bool{}
	var visit func(n uuid.UUID) int
	visit = func(n uuid.UUID) int {
		if members[n] {
			return 0
		}
		members[n] = true
		deepest := 0
		for _, c := range children[n] {
			if h := visit(c.ID); h > deepest {
				deepest = h
			}
		}
		return deepest + 1
	}
	height := visit(id)
	return members, height
}

func projectRow(p repo.Project, components int, roll repo.ProjectRollup) dashboardRow {
	scoreStr, scorePct := "–", "0%"
	if roll.Score != nil {
		scoreStr = fmt.Sprintf("%d", *roll.Score)
		scorePct = fmt.Sprintf("%d%%", clamp(*roll.Score, 0, 100))
	}
	tone := toneOf(roll.Status)
	return dashboardRow{
		Name:       p.Name,
		Subtext:    fmt.Sprintf("%d component%s", components, plural(components, "", "s")),
		Href:       "/projects/" + p.ID.String(),
		Score:      scoreStr,
		ScoreValue: roll.Score,
		ScorePct:   scorePct,
		Tone:       tone,
		BarTone:    tone,
		NodeID:     p.ID.String(),
		Cells:      cellsFor(projectColumns, func(k string) badgeCell { return projectBadge(roll, k) }),
	}
}

func folderRow(f repo.Folder, r repo.FolderRollup) dashboardRow {
	row := dashboardRow{
		IsFolder: true, Name: f.Name, NodeID: f.ID.String(), Subtext: folderSubtext(r),
		Score: "–", ScorePct: "0%", Tone: "none", BarTone: "none",
	}
	if pct, ok := r.Share.Percent(); ok {
		row.Score = fmt.Sprintf("%d / %d passing", r.Share.Passing, r.Share.Measured())
		row.ScoreValue = &pct
		row.ScorePct = fmt.Sprintf("%d%%", pct)
		row.BarTone = rateTone(pct)
		row.Tone = "pass"
		if r.Share.Failing > 0 {
			row.Tone = "fail"
		}
	}
	row.Cells = cellsFor(projectColumns, func(k string) badgeCell { return folderBadge(r.Kinds[k]) })
	return row
}

// folderBadge shows one integration's share across a folder's projects:
// "2/3" means 2 of the 3 projects reporting it pass it.
func folderBadge(ps repo.PassShare) badgeCell {
	pct, ok := ps.Percent()
	if !ok {
		return badgeCell{Text: "–", Tone: "none"}
	}
	return badgeCell{
		Text:  fmt.Sprintf("%d/%d", ps.Passing, ps.Measured()),
		Tone:  rateTone(pct),
		Title: fmt.Sprintf("%d of %d projects reporting this pass it", ps.Passing, ps.Measured()),
		Sort:  &pct,
	}
}

// folderSubtext names what a team should fix next rather than restating
// the number: the failing projects, worst first, when there are any.
func folderSubtext(r repo.FolderRollup) string {
	parts := []string{fmt.Sprintf("%d project%s", r.Projects, plural(r.Projects, "", "s"))}
	if n := len(r.Failing); n > 0 {
		names := make([]string, 0, maxFailingNamed)
		for _, p := range r.Failing[:min(n, maxFailingNamed)] {
			if p.Score != nil {
				names = append(names, fmt.Sprintf("%s %d", p.Name, *p.Score))
			} else {
				names = append(names, p.Name)
			}
		}
		s := "failing: " + strings.Join(names, ", ")
		if n > maxFailingNamed {
			s += fmt.Sprintf(" +%d more", n-maxFailingNamed)
		}
		parts = append(parts, s)
	} else {
		parts = append(parts, fmt.Sprintf("%d component%s", r.Components, plural(r.Components, "", "s")))
	}
	if r.Share.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d not measured", r.Share.Unknown))
	}
	return strings.Join(parts, " · ")
}

func folderDeleteBlocked(projects, subfolders int) string {
	var parts []string
	if subfolders > 0 {
		parts = append(parts, fmt.Sprintf("%d folder%s", subfolders, plural(subfolders, "", "s")))
	}
	if projects > 0 {
		parts = append(parts, fmt.Sprintf("%d project%s", projects, plural(projects, "", "s")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Still holds " + strings.Join(parts, " and ") + " · move them out first"
}

// arrangeProjectTree lays the rows out depth-first: in each folder, its
// subfolders (each followed by its own contents), then its projects. Root
// projects come after the top-level folders.
func arrangeProjectTree(projects []repo.Project, folders []repo.Folder, options []folderOption,
	projectRows map[uuid.UUID]dashboardRow, rollups map[uuid.UUID]repo.FolderRollup) []dashboardRow {

	children := childFolders(folders)
	known := make(map[uuid.UUID]bool, len(folders))
	for _, f := range folders {
		known[f.ID] = true
	}
	filed := make(map[uuid.UUID][]repo.Project, len(folders)+1)
	for _, p := range projects {
		k := parentKey(p.FolderID)
		if !known[k] {
			// Filed in a folder this page did not load: show it at the root
			// rather than let it vanish.
			k = uuid.Nil
		}
		filed[k] = append(filed[k], p)
	}
	for k := range filed {
		sortProjects(filed[k])
	}

	rows := make([]dashboardRow, 0, len(projects)+len(folders))
	seen := make(map[uuid.UUID]bool, len(folders))
	var walk func(parent uuid.UUID, depth int, ancestors []string)
	walk = func(parent uuid.UUID, depth int, ancestors []string) {
		parentID := ""
		if parent != uuid.Nil {
			parentID = parent.String()
		}
		place := func(row dashboardRow) dashboardRow {
			row.ParentID = parentID
			row.Ancestors = strings.Join(ancestors, " ")
			row.IndentPx = depth * treeIndentPx
			return row
		}
		for _, f := range children[parent] {
			if seen[f.ID] {
				continue
			}
			seen[f.ID] = true
			r := rollups[f.ID]
			row := place(folderRow(f, r))
			members, height := folderSubtree(children, f.ID)
			row.MoveValue = parentID
			row.MoveOptions = folderSelect(options, func(o folderOption) bool {
				return members[o.ID] || o.Depth+height > repo.MaxFolderDepth
			})
			// The server's FolderNotEmptyError only counts what is filed
			// DIRECTLY in the folder — direct subfolders and direct
			// projects — not everything at any depth, so the hint must
			// match: r.Projects (the rollup) counts active projects at
			// any depth and would overstate what actually blocks the
			// delete.
			row.DeleteBlocked = folderDeleteBlocked(len(filed[f.ID]), len(children[f.ID]))
			rows = append(rows, row)
			walk(f.ID, depth+1, append(append([]string(nil), ancestors...), f.ID.String()))
		}
		for _, p := range filed[parent] {
			rows = append(rows, place(projectRows[p.ID]))
		}
	}
	walk(uuid.Nil, 0, nil)

	for i := range rows {
		rows[i].N = i + 1
	}
	return rows
}
