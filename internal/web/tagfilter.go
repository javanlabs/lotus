package web

import (
	"context"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The All / tag chips at the top of /dashboard and /projects
// (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §5.3).
// A project matches when it carries any chosen tag; the selection lives in
// the URL as repeated tag parameters, so a filtered view can be bookmarked.

// tagChip is one chip: All or a tag. Href is where clicking it goes.
type tagChip struct {
	Name, Href string
	Active     bool
}

// tagFilter is the parsed selection and the chips that change it. Selected
// empty means All.
type tagFilter struct {
	All      tagChip
	Chips    []tagChip
	Selected []repo.Tag
}

// buildTagFilter matches raw (every tag query value) against tags ignoring
// case. Unknown names are dropped — an old bookmark or a tag removed since
// must not strand the page on an empty fleet — and duplicates collapse.
// Each chip's Href toggles that tag in the current selection, listing the
// remaining tags in tags order; with none left it is the bare path.
func buildTagFilter(path string, raw []string, tags []repo.Tag) tagFilter {
	chosen := make(map[string]bool, len(raw))
	for _, r := range raw {
		chosen[strings.ToLower(strings.TrimSpace(r))] = true
	}
	selected := make(map[uuid.UUID]bool, len(tags))
	f := tagFilter{}
	for _, t := range tags {
		if chosen[strings.ToLower(t.Name)] && !selected[t.ID] {
			selected[t.ID] = true
			f.Selected = append(f.Selected, t)
		}
	}
	href := func(toggle uuid.UUID) string {
		q := url.Values{}
		for _, t := range tags {
			if selected[t.ID] != (t.ID == toggle) {
				q.Add("tag", t.Name)
			}
		}
		if len(q) == 0 {
			return path
		}
		return path + "?" + q.Encode()
	}
	f.All = tagChip{Name: "All", Href: path, Active: len(f.Selected) == 0}
	for _, t := range tags {
		f.Chips = append(f.Chips, tagChip{Name: t.Name, Href: href(t.ID), Active: selected[t.ID]})
	}
	return f
}

// loadTagFilter reads the request's tag parameters against the
// organization's tags.
func loadTagFilter(c *fiber.Ctx, s tenant.Scope, path string) (tagFilter, error) {
	tags, err := repo.ListTags(c.Context(), s)
	if err != nil {
		return tagFilter{}, err
	}
	var raw []string
	for _, v := range c.Context().QueryArgs().PeekMulti("tag") {
		raw = append(raw, string(v))
	}
	return buildTagFilter(path, raw, tags), nil
}

// projectFilter is the set of projects the selection keeps: nil when All,
// so callers can tell "no filter" from "matches nothing".
func (f tagFilter) projectFilter(ctx context.Context, s tenant.Scope) (map[uuid.UUID]bool, error) {
	if len(f.Selected) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(f.Selected))
	for i, t := range f.Selected {
		ids[i] = t.ID
	}
	projects, err := repo.ProjectIDsWithAnyTag(ctx, s, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(projects))
	for _, id := range projects {
		out[id] = true
	}
	return out, nil
}

// projectIDList is a projectFilter set as the slice the repo's
// projectIDs parameters take: nil stays nil (every project).
func projectIDList(set map[uuid.UUID]bool) []uuid.UUID {
	if set == nil {
		return nil
	}
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
