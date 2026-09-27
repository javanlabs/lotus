package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
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
