package web

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaScenarioGroupView is one group on the scenario page. The first one is
// the unnamed group (ID nil) holding the items that have no group.
type qaScenarioGroupView struct {
	ID    *uuid.UUID
	Name  string
	Items []qaScenarioItemView
	First bool // first named group: cannot move up
	Last  bool // last named group: cannot move down
}

type qaScenarioItemView struct {
	repo.QAScenarioEntry
	Href  string
	First bool // first item of its group
	Last  bool // last item of its group
}

type qaScenarioData struct {
	Project      repo.Project
	Base         string
	Scenario     repo.QAScenario
	PhaseLabel   string
	Groups       []qaScenarioGroupView
	Cases        int
	ArchivedCase int
	CanWrite     bool
	PhaseOptions []httpx.SelectOption
	GroupOptions []httpx.SelectOption
	// SectionOptions filter the Add cases picker.
	SectionOptions []httpx.SelectOption
}

// qaScenarioGroups arranges entries under their groups: the unnamed group
// first, then the named groups by position (empty ones too).
func qaScenarioGroups(base string, groups []repo.QAScenarioGroup, entries []repo.QAScenarioEntry) []qaScenarioGroupView {
	out := []qaScenarioGroupView{{}}
	index := map[uuid.UUID]int{}
	for i, g := range groups {
		id := g.ID
		index[id] = len(out)
		out = append(out, qaScenarioGroupView{ID: &id, Name: g.Name, First: i == 0, Last: i == len(groups)-1})
	}
	for _, e := range entries {
		i := 0
		if e.GroupID != nil {
			i = index[*e.GroupID]
		}
		out[i].Items = append(out[i].Items, qaScenarioItemView{QAScenarioEntry: e, Href: base + "/cases/" + strconv.Itoa(e.CaseKey)})
	}
	for gi := range out {
		for ii := range out[gi].Items {
			out[gi].Items[ii].First = ii == 0
			out[gi].Items[ii].Last = ii == len(out[gi].Items)-1
		}
	}
	return out
}

func qaScenarioPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sc, err := loadQAScenario(c, s, project)
		if err != nil {
			return err
		}
		groups, err := repo.ListQAScenarioGroups(c.Context(), s, sc.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		entries, err := repo.ListQAScenarioEntries(c.Context(), s, sc.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data := qaScenarioData{
			Project: *project, Base: qaBase(project), Scenario: *sc, PhaseLabel: qaPhaseLabels[sc.Phase],
			Groups: qaScenarioGroups(qaBase(project), groups, entries), Cases: len(entries),
			CanWrite: mustPrincipal(c).Can("qa.case.write") && sc.ArchivedAt == nil, PhaseOptions: qaPhaseOptions(),
			GroupOptions: []httpx.SelectOption{{Value: "", Label: "No group"}},
		}
		for _, e := range entries {
			if e.CaseArchived {
				data.ArchivedCase++
			}
		}
		if data.CanWrite {
			sections, err := repo.ListQASections(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			data.SectionOptions = qaSectionOptions(qaSectionTree(sections, nil, false, nil), nil)
		}
		for _, g := range groups {
			data.GroupOptions = append(data.GroupOptions, httpx.SelectOption{Value: g.ID.String(), Label: g.Name})
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Scenarios", Href: data.Base + "/scenarios"}, httpx.NavCrumb{Label: sc.Name})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_scenario.html", httpx.ViewData{
			Title: sc.Name + " · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

// Editing a scenario: groups, cases, order. Every handler loads the
// scenario through writableQAScenario (it must belong to :id and be live)
// and checks any group or case id it is given against the scenario or the
// project; the repo skips item ids of other scenarios on its own.

const qaPickerLimit = 50

// writableQAScenario loads :sid for a write. ok is false when the handler
// must stop: err is then either a 404 or the "archived" rejection, already
// written, to return as is.
func writableQAScenario(c *fiber.Ctx, d Deps) (tenant.Scope, *repo.Project, *repo.QAScenario, bool, error) {
	s, project, err := loadQAProject(c)
	if err != nil {
		return s, nil, nil, false, err
	}
	sc, err := loadQAScenario(c, s, project)
	if err != nil {
		return s, nil, nil, false, err
	}
	if sc.ArchivedAt != nil {
		return s, project, sc, false, qaFormError(c, d, fiber.StatusConflict, "This scenario is archived.")
	}
	return s, project, sc, true, nil
}

func scenarioURL(project *repo.Project, sc *repo.QAScenario) string {
	return qaBase(project) + "/scenarios/" + sc.ID.String()
}

// formUUIDs reads a repeated form field of ids. A malformed id is 404.
func formUUIDs(c *fiber.Ctx, field string) ([]uuid.UUID, error) {
	raw := c.Context().PostArgs().PeekMulti(field)
	out := make([]uuid.UUID, 0, len(raw))
	for _, r := range raw {
		id, err := uuid.Parse(string(r))
		if err != nil {
			return nil, fiber.ErrNotFound
		}
		out = append(out, id)
	}
	return out, nil
}

// scenarioGroup reads a group id (form or :gid) that must belong to sc.
// "" is no group.
func scenarioGroup(c *fiber.Ctx, s tenant.Scope, sc *repo.QAScenario, raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	groups, err := repo.ListQAScenarioGroups(c.Context(), s, sc.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.ID == id {
			return &id, nil
		}
	}
	return nil, fiber.ErrNotFound
}

func groupNameErr(err error) (int, string, bool) {
	if errors.Is(err, repo.ErrInvalidInput) {
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Group name is required and can be at most %d characters.", repo.MaxQANameLength), true
	}
	return 0, "", false
}

func addQAScenarioGroupUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		if _, err := repo.CreateQAScenarioGroup(c.Context(), s, sc.ID, c.FormValue("name")); err != nil {
			if status, msg, ok := groupNameErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

func renameQAScenarioGroupUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		gid, err := scenarioGroup(c, s, sc, c.Params("gid"))
		if err != nil || gid == nil {
			return fiber.ErrNotFound
		}
		if err := repo.RenameQAScenarioGroup(c.Context(), s, sc.ID, *gid, c.FormValue("name")); err != nil {
			if status, msg, ok := groupNameErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

// swapped returns ids with the element at i moved one step in dir ("up" or
// "down"); ok is false when it cannot move that way.
func swapped(ids []uuid.UUID, i int, dir string) ([]uuid.UUID, bool) {
	j := i + 1
	if dir == "up" {
		j = i - 1
	}
	if i < 0 || j < 0 || j >= len(ids) {
		return ids, false
	}
	out := append([]uuid.UUID(nil), ids...)
	out[i], out[j] = out[j], out[i]
	return out, true
}

func moveQAScenarioGroupUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		gid, err := scenarioGroup(c, s, sc, c.Params("gid"))
		if err != nil || gid == nil {
			return fiber.ErrNotFound
		}
		groups, err := repo.ListQAScenarioGroups(c.Context(), s, sc.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		ids, at := make([]uuid.UUID, len(groups)), -1
		for i, g := range groups {
			ids[i] = g.ID
			if g.ID == *gid {
				at = i
			}
		}
		if order, moved := swapped(ids, at, c.FormValue("dir")); moved {
			if err := repo.ReorderQAScenarioGroups(c.Context(), s, sc.ID, order); err != nil {
				return writeRepoErr(c, err)
			}
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

func addQAScenarioCasesUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		ids, err := formUUIDs(c, "case_id")
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one case.")
		}
		gid, err := scenarioGroup(c, s, sc, c.FormValue("group_id"))
		if err != nil {
			return err
		}
		if _, err := repo.AddQAScenarioCases(c.Context(), s, sc.ID, gid, ids); err != nil {
			if errors.Is(err, repo.ErrInvalidInput) {
				return qaFormError(c, d, fiber.StatusNotFound, "One of those cases is not in this project.")
			}
			return err
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

func removeQAScenarioCasesUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		ids, err := formUUIDs(c, "case_id")
		if err != nil {
			return err
		}
		if err := repo.RemoveQAScenarioCases(c.Context(), s, sc.ID, ids); err != nil {
			return writeRepoErr(c, err)
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

// moveQAScenarioItemUI moves one item a step up or down within its group.
func moveQAScenarioItemUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		itemID, err := uuid.Parse(c.Params("iid"))
		if err != nil {
			return fiber.ErrNotFound
		}
		entries, err := repo.ListQAScenarioEntries(c.Context(), s, sc.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		ids, at := make([]uuid.UUID, len(entries)), -1
		for i, e := range entries {
			ids[i] = e.ItemID
			if e.ItemID == itemID {
				at = i
			}
		}
		if at < 0 {
			return fiber.ErrNotFound
		}
		order, moved := swapped(ids, at, c.FormValue("dir"))
		// Only within the group: the neighbour must share it.
		if moved {
			j := at + 1
			if c.FormValue("dir") == "up" {
				j = at - 1
			}
			if uuidString(entries[j].GroupID) != uuidString(entries[at].GroupID) {
				moved = false
			}
		}
		if moved {
			if err := repo.ReorderQAScenario(c.Context(), s, sc.ID, order); err != nil {
				return writeRepoErr(c, err)
			}
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

func regroupQAScenarioItemsUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		ids, err := formUUIDs(c, "item_id")
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one case.")
		}
		gid, err := scenarioGroup(c, s, sc, c.FormValue("group_id"))
		if err != nil {
			return err
		}
		if _, err := repo.SetQAScenarioItemsGroup(c.Context(), s, sc.ID, ids, gid); err != nil {
			return writeRepoErr(c, err)
		}
		return redirectAfterMutation(c, scenarioURL(project, sc))
	}
}

type qaPickerData struct {
	Rows      []repo.QACase
	More      bool
	Searching bool
}

// qaScenarioPickerUI is the Add cases dialog's result list, an htmx
// fragment: live cases matching q and section that the scenario does not
// hold yet, at most qaPickerLimit of them.
func qaScenarioPickerUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sc, err := loadQAScenario(c, s, project)
		if err != nil {
			return err
		}
		filter := repo.QACaseFilter{IncludeSubsections: true, Query: c.Query("q"), Limit: 4 * qaPickerLimit}
		if raw := c.Query("section"); raw != "" {
			id, err := qaOwned(c, s, project, repo.QAKindSection, raw)
			if err != nil {
				return err
			}
			filter.SectionID = &id
		}
		entries, err := repo.ListQAScenarioEntries(c.Context(), s, sc.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		in := make(map[uuid.UUID]bool, len(entries))
		for _, e := range entries {
			in[e.CaseID] = true
		}
		cases, err := repo.ListQACases(c.Context(), s, project.ID, filter)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data := qaPickerData{Searching: filter.Query != "" || filter.SectionID != nil}
		for _, qc := range cases {
			if in[qc.ID] {
				continue
			}
			if len(data.Rows) == qaPickerLimit {
				data.More = true
				break
			}
			data.Rows = append(data.Rows, qc)
		}
		c.Type("html")
		return d.Renderer.RenderNamed(c, "qa_case_picker", data)
	}
}
