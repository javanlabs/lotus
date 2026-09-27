package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// qaCasesPerPage bounds the library table. Imported projects hold up to a
// few thousand cases; the tree and the search box are how a reader narrows
// them, the pager only walks what is left.
const qaCasesPerPage = 100

// qaTreeRow is one section in the library's tree, in display order.
type qaTreeRow struct {
	ID       uuid.UUID
	Name     string
	Path     string // "Auth / Login"
	Depth    int    // 0 at the top level
	Count    int    // live cases in the section and everything below it
	Archived bool
	Active   bool
	Href     string
}

// Indent is the row's left padding in px: one step per level.
func (r qaTreeRow) Indent() int { return 10 + 14*r.Depth }

// qaSectionTree flattens the sections into display order — depth first,
// siblings in the repo's order — with subtree counts. Archived sections are
// left out unless includeArchived.
func qaSectionTree(sections []repo.QASection, counts map[uuid.UUID]int, includeArchived bool, active *uuid.UUID) []qaTreeRow {
	children := map[uuid.UUID][]repo.QASection{}
	var roots []repo.QASection
	for _, s := range sections {
		if s.ArchivedAt != nil && !includeArchived {
			continue
		}
		if s.ParentID == nil {
			roots = append(roots, s)
		} else {
			children[*s.ParentID] = append(children[*s.ParentID], s)
		}
	}
	var out []qaTreeRow
	var walk func(s repo.QASection, depth int, path string) int
	walk = func(s repo.QASection, depth int, path string) int {
		if path != "" {
			path += " / "
		}
		path += s.Name
		i := len(out)
		out = append(out, qaTreeRow{
			ID: s.ID, Name: s.Name, Path: path, Depth: depth, Archived: s.ArchivedAt != nil,
			Active: active != nil && *active == s.ID,
		})
		total := counts[s.ID]
		for _, c := range children[s.ID] {
			total += walk(c, depth+1, path)
		}
		out[i].Count = total
		return total
	}
	for _, r := range roots {
		walk(r, 0, "")
	}
	return out
}

type qaCaseRow struct {
	repo.QACase
	Href string
}

type qaCasesData struct {
	Project     repo.Project
	Base        string
	Tree        []qaTreeRow
	AllCount    int
	AllHref     string
	SectionID   string // "" = all cases
	SectionPath string
	Query       string
	Archived    bool
	Rows        []qaCaseRow
	Total       int
	Page, Pages int
	From, To    int
	PrevHref    string
	NextHref    string
	ArchiveHref string // the same view with the archived toggle flipped
}

// casesURL is the library URL for a section, search, archived toggle and page.
func casesURL(base, section, q string, archived bool, page int) string {
	v := url.Values{}
	if section != "" {
		v.Set("section", section)
	}
	if q != "" {
		v.Set("q", q)
	}
	if archived {
		v.Set("archived", "1")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return base + "/cases"
	}
	return base + "/cases?" + v.Encode()
}

// qaCasesPage is GET /projects/:id/qa/cases: the section tree beside the
// cases of the chosen section (and its subsections), searchable and paged.
func qaCasesPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		data := qaCasesData{
			Project: *project, Base: qaBase(project),
			Query:    strings.TrimSpace(c.Query("q")),
			Archived: c.Query("archived") == "1",
		}
		filter := repo.QACaseFilter{IncludeSubsections: true, IncludeArchived: data.Archived, Query: data.Query}
		var active *uuid.UUID
		if raw := c.Query("section"); raw != "" {
			id, err := qaOwned(c, s, project, repo.QAKindSection, raw)
			if err != nil {
				return err
			}
			active, filter.SectionID, data.SectionID = &id, &id, id.String()
		}

		sections, err := repo.ListQASections(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		counts, err := repo.QASectionCaseCounts(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data.Tree = qaSectionTree(sections, counts, data.Archived, active)
		data.AllHref = casesURL(data.Base, "", "", data.Archived, 1)
		for i, r := range data.Tree {
			data.Tree[i].Href = casesURL(data.Base, r.ID.String(), "", data.Archived, 1)
			if r.Depth == 0 {
				data.AllCount += r.Count
			}
			if r.Active {
				data.SectionPath = r.Path
			}
		}

		data.Total, err = repo.CountQACases(c.Context(), s, project.ID, filter)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data.Pages = max(1, (data.Total+qaCasesPerPage-1)/qaCasesPerPage)
		data.Page, _ = strconv.Atoi(c.Query("page"))
		data.Page = min(max(data.Page, 1), data.Pages)
		filter.Limit, filter.Offset = qaCasesPerPage, (data.Page-1)*qaCasesPerPage
		cases, err := repo.ListQACases(c.Context(), s, project.ID, filter)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, qc := range cases {
			data.Rows = append(data.Rows, qaCaseRow{QACase: qc, Href: data.Base + "/cases/" + strconv.Itoa(qc.Key)})
		}
		if data.Total > 0 {
			data.From, data.To = filter.Offset+1, filter.Offset+len(cases)
		}
		if data.Page > 1 {
			data.PrevHref = casesURL(data.Base, data.SectionID, data.Query, data.Archived, data.Page-1)
		}
		if data.Page < data.Pages {
			data.NextHref = casesURL(data.Base, data.SectionID, data.Query, data.Archived, data.Page+1)
		}
		data.ArchiveHref = casesURL(data.Base, data.SectionID, data.Query, !data.Archived, 1)

		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Cases"})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_cases.html", httpx.ViewData{
			Title: "Cases · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}
