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

// qaIssuesPerPage bounds the issue list: production projects hold up to
// 579 issues. Filters and the search box narrow it; the pager walks the rest.
const qaIssuesPerPage = 100

// qaClosedIssueStatuses are the statuses that need no more work.
var qaClosedIssueStatuses = []string{"closed", "invalid", "duplicate"}

// qaSeverityTone: the two severities that stop testing read as failures.
func qaSeverityTone(sev string) string {
	if sev == "blocker" || sev == "critical" {
		return "fail"
	}
	return "none"
}

type qaIssueRow struct {
	repo.QAIssue
	Href         string
	SeverityTone string
	Assignee     string
	PlanName     string
}

type qaIssuesData struct {
	Project     repo.Project
	Base        string
	Rows        []qaIssueRow
	Status      string // "open" (default), "closed", "all"
	Severity    string
	Source      string
	Plan        string
	Query       string
	Total       int
	Page, Pages int
	From, To    int
	PrevHref    string
	NextHref    string
	CanReport   bool

	StatusOptions   []httpx.SelectOption
	SeverityOptions []httpx.SelectOption
	SourceOptions   []httpx.SelectOption
	PlanOptions     []httpx.SelectOption
}

// url is the list with the current filters and page p.
func (d qaIssuesData) url(p int) string {
	v := url.Values{}
	for k, val := range map[string]string{"status": d.Status, "severity": d.Severity, "source": d.Source, "plan": d.Plan, "q": d.Query} {
		if val != "" && !(k == "status" && val == "open") {
			v.Set(k, val)
		}
	}
	if p > 1 {
		v.Set("page", strconv.Itoa(p))
	}
	if len(v) == 0 {
		return d.Base + "/issues"
	}
	return d.Base + "/issues?" + v.Encode()
}

func qaSeverityOptions(any string) []httpx.SelectOption {
	out := []httpx.SelectOption{}
	if any != "" {
		out = append(out, httpx.SelectOption{Value: "", Label: any})
	}
	for _, s := range repo.QAIssueSeverities {
		out = append(out, httpx.SelectOption{Value: s, Label: s})
	}
	return out
}

func qaIssuesPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		data := qaIssuesData{Project: *project, Base: qaBase(project),
			Status: c.Query("status", "open"), Severity: c.Query("severity"), Source: c.Query("source"),
			Plan: c.Query("plan"), Query: strings.TrimSpace(c.Query("q")),
			CanReport: mustPrincipal(c).Can("qa.issue.report")}
		filter := repo.QAIssueFilter{Query: data.Query}
		switch data.Status {
		case "closed":
			filter.Statuses = qaClosedIssueStatuses
		case "all":
		default:
			data.Status, filter.Statuses = "open", repo.QAOpenIssueStatuses
		}
		for _, sev := range repo.QAIssueSeverities {
			if sev == data.Severity {
				filter.Severity = sev
			}
		}
		if data.Source == "internal" || data.Source == "client" {
			filter.Source = data.Source
		} else {
			data.Source = ""
		}
		if data.Plan != "" {
			id, err := qaOwned(c, s, project, repo.QAKindPlan, data.Plan)
			if err != nil {
				return err
			}
			filter.PlanID = &id
		}
		plans, err := repo.ListQAPlans(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		planNames := map[uuid.UUID]string{}
		data.PlanOptions = []httpx.SelectOption{{Value: "", Label: "Any plan"}}
		for _, p := range plans {
			planNames[p.ID] = p.Name
			data.PlanOptions = append(data.PlanOptions, httpx.SelectOption{Value: p.ID.String(), Label: p.Name})
		}
		data.StatusOptions = []httpx.SelectOption{{Value: "open", Label: "Open"}, {Value: "closed", Label: "Closed"}, {Value: "all", Label: "All"}}
		data.SeverityOptions = qaSeverityOptions("Any severity")
		data.SourceOptions = []httpx.SelectOption{{Value: "", Label: "Any source"}, {Value: "internal", Label: "internal"}, {Value: "client", Label: "client"}}

		if data.Total, err = repo.CountQAIssues(c.Context(), s, project.ID, filter); err != nil {
			return writeRepoErr(c, err)
		}
		data.Pages = max(1, (data.Total+qaIssuesPerPage-1)/qaIssuesPerPage)
		data.Page, _ = strconv.Atoi(c.Query("page"))
		data.Page = min(max(data.Page, 1), data.Pages)
		filter.Limit, filter.Offset = qaIssuesPerPage, (data.Page-1)*qaIssuesPerPage
		issues, err := repo.ListQAIssues(c.Context(), s, project.ID, filter)
		if err != nil {
			return writeRepoErr(c, err)
		}
		labels, err := qaPeopleLabels(c, d, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, is := range issues {
			row := qaIssueRow{QAIssue: is, Href: data.Base + "/issues/" + strconv.Itoa(is.Key),
				SeverityTone: qaSeverityTone(is.Severity), Assignee: personLabel(labels, is.AssigneeID)}
			if is.PlanID != nil {
				row.PlanName = planNames[*is.PlanID]
			}
			data.Rows = append(data.Rows, row)
		}
		if data.Total > 0 {
			data.From, data.To = filter.Offset+1, filter.Offset+len(issues)
		}
		if data.Page > 1 {
			data.PrevHref = data.url(data.Page - 1)
		}
		if data.Page < data.Pages {
			data.NextHref = data.url(data.Page + 1)
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Issues"})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_issues.html", httpx.ViewData{
			Title: "Issues · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}
