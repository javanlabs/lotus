package web

import (
	"maps"
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
	ParentID *uuid.UUID
	Name     string
	Path     string // "Auth / Login"
	Depth    int    // 0 at the top level
	Count    int    // live cases in the section and everything below it
	Archived bool
	Active   bool
	Href     string
	PIC      string // the section's own PIC, named; "" for none
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
			ID: s.ID, ParentID: s.ParentID, Name: s.Name, Path: path, Depth: depth, Archived: s.ArchivedAt != nil,
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
	PIC  *qaPICView // effective: the case's own, else its section's
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
	PIC         string // the PIC filter: "", "me", "none" or a user id
	PICOptions  []httpx.SelectOption
	SectionPIC  *qaPICView // the chosen section's PIC, own or inherited

	// Write controls, shown only with qa.case.write.
	CanWrite       bool
	SectionOptions []httpx.SelectOption // every live section, for "inside" and bulk move
	Active         *qaTreeRow           // the chosen section, for its edit dialog
	ParentValue    string               // the chosen section's parent, "" at the top level
	MoveOptions    []httpx.SelectOption // where the chosen section may move: never into itself or below
	NewPICOptions  []httpx.SelectOption // PIC of a new section
	ActivePIC      string               // the chosen section's own PIC, for its edit dialog
	ActivePICOpts  []httpx.SelectOption
}

// qaSectionOptions are the tree's live sections as picker options, minus
// skip and everything below it (when skip is set).
func qaSectionOptions(tree []qaTreeRow, skip *uuid.UUID) []httpx.SelectOption {
	out := []httpx.SelectOption{}
	skipDepth := -1
	for _, r := range tree {
		if skipDepth >= 0 {
			if r.Depth > skipDepth {
				continue
			}
			skipDepth = -1
		}
		if skip != nil && r.ID == *skip {
			skipDepth = r.Depth
			continue
		}
		if r.Archived {
			continue
		}
		out = append(out, httpx.SelectOption{Value: r.ID.String(), Label: r.Path, Name: r.Name, Depth: r.Depth})
	}
	return out
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

// withPIC adds the PIC filter to a library URL built by casesURL, so
// moving between sections, pages and the archived view keeps it.
func withPIC(u, pic string) string {
	if pic == "" {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "pic=" + url.QueryEscape(pic)
}

// qaPICFilter reads ?pic= into the filter: "me" is the signed-in user,
// "none" the cases nobody is in charge of. Anything else must be a user id.
func qaPICFilter(c *fiber.Ctx, raw string, f *repo.QACaseFilter) error {
	switch raw {
	case "":
	case "none":
		f.PICNone = true
	case "me":
		me := mustPrincipal(c).UserID
		f.PICID = &me
	default:
		id, err := uuid.Parse(raw)
		if err != nil {
			return fiber.ErrNotFound
		}
		f.PICID = &id
	}
	return nil
}

// qaCasesPage is GET /projects/:id/qa/cases: the section tree beside the
// cases of the chosen section (and its subsections), searchable, filtered
// by PIC and paged.
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
			PIC:      strings.TrimSpace(c.Query("pic")),
		}
		filter := repo.QACaseFilter{IncludeSubsections: true, IncludeArchived: data.Archived, Query: data.Query}
		if err := qaPICFilter(c, data.PIC, &filter); err != nil {
			return err
		}
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
		// An archived section is only shown with the archived rows; its URL
		// on its own (a bookmark, the page it was archived from) opens that view.
		if active != nil && !data.Archived {
			for _, sec := range sections {
				if sec.ID == *active && sec.ArchivedAt != nil {
					return c.Redirect(withPIC(casesURL(data.Base, data.SectionID, data.Query, true, 1), data.PIC), fiber.StatusFound)
				}
			}
		}
		counts, err := repo.QASectionCaseCounts(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		people, err := qaPeople(c, d, s, project)
		if err != nil {
			return writeRepoErr(c, err)
		}
		var filterPIC *uuid.UUID // a person picked in the filter, to name
		if filter.PICID != nil && data.PIC != "me" {
			filterPIC = filter.PICID
		}
		labels, err := qaUserLabels(c, d, qaPICIDs(sections, filterPIC))
		if err != nil {
			return writeRepoErr(c, err)
		}
		picOf := map[uuid.UUID]*uuid.UUID{}
		for _, sec := range sections {
			picOf[sec.ID] = sec.PicID
		}
		data.Tree = qaSectionTree(sections, counts, data.Archived, active)
		data.AllHref = withPIC(casesURL(data.Base, "", "", data.Archived, 1), data.PIC)
		for i, r := range data.Tree {
			data.Tree[i].Href = withPIC(casesURL(data.Base, r.ID.String(), "", data.Archived, 1), data.PIC)
			data.Tree[i].PIC = personLabel(labels, picOf[r.ID])
			if r.Depth == 0 {
				data.AllCount += r.Count
			}
			if r.Active {
				data.SectionPath = r.Path
				if v := qaEffectivePICView(sections, r.ID, picOf[r.ID], labels); v != nil {
					data.SectionPIC = v
				}
			}
		}
		data.PICOptions = qaPICOptions(people, "Anyone", filterPIC, labels)
		data.PICOptions = append(data.PICOptions[:1:1], append([]httpx.SelectOption{
			{Value: "me", Label: "Me"}, {Value: "none", Label: "Nobody"}}, data.PICOptions[1:]...)...)

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
		var casePICs []*uuid.UUID
		for _, qc := range cases {
			casePICs = append(casePICs, qc.PicID)
		}
		if more := qaPICIDs(nil, casePICs...); len(more) > 0 {
			extra, err := qaUserLabels(c, d, more)
			if err != nil {
				return writeRepoErr(c, err)
			}
			maps.Copy(labels, extra)
		}
		for _, qc := range cases {
			data.Rows = append(data.Rows, qaCaseRow{QACase: qc, Href: data.Base + "/cases/" + strconv.Itoa(qc.Key),
				PIC: qaEffectivePICView(sections, qc.SectionID, qc.PicID, labels)})
		}
		if data.Total > 0 {
			data.From, data.To = filter.Offset+1, filter.Offset+len(cases)
		}
		if data.Page > 1 {
			data.PrevHref = withPIC(casesURL(data.Base, data.SectionID, data.Query, data.Archived, data.Page-1), data.PIC)
		}
		if data.Page < data.Pages {
			data.NextHref = withPIC(casesURL(data.Base, data.SectionID, data.Query, data.Archived, data.Page+1), data.PIC)
		}
		data.ArchiveHref = withPIC(casesURL(data.Base, data.SectionID, data.Query, !data.Archived, 1), data.PIC)
		if mustPrincipal(c).Can("qa.case.write") {
			data.CanWrite = true
			data.SectionOptions = qaSectionOptions(data.Tree, nil)
			data.NewPICOptions = qaPICOptions(people, "Nobody", nil, labels)
			for i := range data.Tree {
				if data.Tree[i].Active && !data.Tree[i].Archived {
					data.Active = &data.Tree[i]
					data.ParentValue = uuidString(data.Tree[i].ParentID)
					data.MoveOptions = qaSectionOptions(data.Tree, &data.Tree[i].ID)
					data.ActivePIC = uuidString(picOf[data.Tree[i].ID])
					data.ActivePICOpts = qaPICOptions(people, "Nobody", picOf[data.Tree[i].ID], labels)
				}
			}
		}

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
