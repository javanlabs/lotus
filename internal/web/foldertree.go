package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

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

// folderSubtext sizes a folder: how many projects it holds, how many of
// them fail and how many are not measured yet. The failing projects are
// counted, not named — the "Failing" filter lists them.
func folderSubtext(r repo.FolderRollup) string {
	parts := []string{fmt.Sprintf("%d project%s", r.Projects, plural(r.Projects, "", "s"))}
	if n := len(r.Failing); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failing", n))
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

// crumb is one step of the folder view's breadcrumb. FolderID is "" for
// the top level; it is also what a drop on the crumb moves things into.
type crumb struct {
	Name     string
	Href     string
	FolderID string
	Current  bool
}

// folderTrail puts the folder path into the page's breadcrumb (the shell's
// trail, after the organization) — nothing at the top level, where the
// organization is already the last crumb. The top level itself is the
// organization's name, so the view's own "Projects" crumb is left out.
// Every crumb but the open folder links back up and, when canDrop, accepts
// drag-and-drop moves.
func folderTrail(crumbs []crumb, canDrop bool) []httpx.NavCrumb {
	if len(crumbs) < 2 {
		return nil
	}
	out := make([]httpx.NavCrumb, 0, len(crumbs)-1)
	for _, c := range crumbs[1:] {
		if c.Current {
			out = append(out, httpx.NavCrumb{Label: c.Name})
			continue
		}
		out = append(out, httpx.NavCrumb{Label: c.Name, Href: c.Href, Drop: canDrop, DropFolder: c.FolderID})
	}
	return out
}

// folderHref opens a folder in the folder view.
func folderHref(id uuid.UUID) string { return "/projects?folder=" + id.String() }

// folderView is the organization's folder tree seen from one folder
// (uuid.Nil = the top level).
type folderView struct {
	current  uuid.UUID
	folders  map[uuid.UUID]repo.Folder
	children map[uuid.UUID][]repo.Folder
	filed    map[uuid.UUID][]repo.Project // by folder; uuid.Nil = top level
	paths    map[uuid.UUID]string
	options  []folderOption
}

func newFolderView(projects []repo.Project, folders []repo.Folder, options []folderOption, current uuid.UUID) folderView {
	v := folderView{
		current:  current,
		folders:  make(map[uuid.UUID]repo.Folder, len(folders)),
		children: childFolders(folders),
		filed:    make(map[uuid.UUID][]repo.Project, len(folders)+1),
		paths:    make(map[uuid.UUID]string, len(options)),
		options:  options,
	}
	for _, f := range folders {
		v.folders[f.ID] = f
	}
	for _, o := range options {
		v.paths[o.ID] = o.Path
	}
	for _, p := range projects {
		k := parentKey(p.FolderID)
		if _, known := v.folders[k]; !known {
			// Filed in a folder this page did not load: show it at the top
			// level rather than let it vanish.
			k = uuid.Nil
		}
		v.filed[k] = append(v.filed[k], p)
	}
	for k := range v.filed {
		sortProjects(v.filed[k])
	}
	return v
}

func (v folderView) currentID() string {
	if v.current == uuid.Nil {
		return ""
	}
	return v.current.String()
}

// levelRows is what the "All" filter shows: the current folder's
// subfolders, then the projects filed directly in it.
func (v folderView) levelRows(projectRows map[uuid.UUID]dashboardRow, rollups map[uuid.UUID]repo.FolderRollup) []dashboardRow {
	rows := make([]dashboardRow, 0, len(v.children[v.current])+len(v.filed[v.current]))
	for _, f := range v.children[v.current] {
		r := rollups[f.ID]
		row := folderRow(f, r)
		row.Href = folderHref(f.ID)
		members, height := folderSubtree(v.children, f.ID)
		row.MoveValue = v.currentID()
		row.MoveOptions = folderSelect(v.options, func(o folderOption) bool {
			return members[o.ID] || o.Depth+height > repo.MaxFolderDepth
		})
		// The server's FolderNotEmptyError only counts what is filed
		// DIRECTLY in the folder — direct subfolders and direct projects —
		// so the hint counts the same, not the rollup's any-depth total.
		row.DeleteBlocked = folderDeleteBlocked(len(v.filed[f.ID]), len(v.children[f.ID]))
		rows = append(rows, row)
	}
	for _, p := range v.filed[v.current] {
		rows = append(rows, projectRows[p.ID])
	}
	for i := range rows {
		rows[i].N = i + 1
	}
	return rows
}

// beneath reports whether a project filed in folderID sits under the
// current folder, at any depth.
func (v folderView) beneath(folderID uuid.UUID) bool {
	if v.current == uuid.Nil {
		return true
	}
	for id, steps := folderID, 0; id != uuid.Nil && steps <= len(v.folders); steps++ {
		if id == v.current {
			return true
		}
		id = parentKey(v.folders[id].ParentID)
	}
	return false
}

func (v folderView) projectsBeneath() int {
	n := 0
	for k, ps := range v.filed {
		if v.beneath(k) {
			n += len(ps)
		}
	}
	return n
}

// failingRows is what the "Failing" filter shows: every failing project
// beneath the current folder at any depth, worst score first, each row
// saying where the project lives.
func (v folderView) failingRows(projectRows map[uuid.UUID]dashboardRow) []dashboardRow {
	var rows []dashboardRow
	for k, ps := range v.filed {
		if !v.beneath(k) {
			continue
		}
		where := "Top level"
		if k != uuid.Nil {
			where = v.paths[k]
		}
		for _, p := range ps {
			row := projectRows[p.ID]
			if row.Tone != "fail" {
				continue
			}
			row.Subtext = where + " · " + row.Subtext
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].ScoreValue, rows[j].ScoreValue
		if (a == nil) != (b == nil) {
			return b == nil
		}
		if a != nil && *a != *b {
			return *a < *b
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	for i := range rows {
		rows[i].N = i + 1
	}
	return rows
}

// crumbs is the path from the top level down to the current folder.
func (v folderView) crumbs() []crumb {
	var path []repo.Folder
	for id, steps := v.current, 0; id != uuid.Nil && steps <= len(v.folders); steps++ {
		f, ok := v.folders[id]
		if !ok {
			break
		}
		path = append([]repo.Folder{f}, path...)
		id = parentKey(f.ParentID)
	}
	out := []crumb{{Name: "Projects", Href: "/projects", Current: len(path) == 0}}
	for i, f := range path {
		out = append(out, crumb{Name: f.Name, Href: folderHref(f.ID), FolderID: f.ID.String(), Current: i == len(path)-1})
	}
	return out
}
