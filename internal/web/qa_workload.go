package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// The workload page's rows travel to the browser as one JSON island and
// are searched, sorted and paged there (lotus.js qaWorkload): a month's
// people and projects are tens of rows, not thousands.

type qaWLCounts struct {
	Total    int `json:"total"`
	Untested int `json:"untested"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Blocked  int `json:"blocked"`
	Skipped  int `json:"skipped"`
}

func wlCounts(c repo.QAWorkloadCounts) qaWLCounts {
	return qaWLCounts{Total: c.Total, Untested: c.Untested, Passed: c.Passed, Failed: c.Failed, Blocked: c.Blocked, Skipped: c.Skipped}
}

type qaWLRun struct {
	Name string `json:"name"`
	Href string `json:"href"`
	qaWLCounts
}

type qaWLRunGroup struct {
	Project string    `json:"project"`
	Href    string    `json:"href"`
	Runs    []qaWLRun `json:"runs"`
}

type qaWLIssue struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Project  string `json:"project"`
	Reporter string `json:"reporter"`
	Status   string `json:"status"`
	Href     string `json:"href"`
}

type qaWLPersonRow struct {
	Name     string         `json:"name"`
	Role     string         `json:"role"`
	Projects int            `json:"projects"`
	Cases    int            `json:"cases"`
	Issues   int            `json:"issues"`
	Detail   []qaWLRunGroup `json:"detail"`
	Reported []qaWLIssue    `json:"reported"`
	qaWLCounts
}

type qaWLProjectRow struct {
	Name     string      `json:"name"`
	Href     string      `json:"href"`
	Cases    int         `json:"cases"`
	Runs     int         `json:"runs"`
	Issues   int         `json:"issues"`
	RunList  []qaWLRun   `json:"runList"`
	Reported []qaWLIssue `json:"reported"`
	qaWLCounts
}

type qaWorkloadData struct {
	Month      string // YYYY-MM
	MonthLabel string // September 2026
	PrevHref   string
	NextHref   string // "" when the month shown is the current one
	Unknown    string // a ?month= that did not parse, shown as a notice
	People     []qaWLPersonRow
	Projects   []qaWLProjectRow
}

func qaWorkloadPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		current, err := repo.CurrentQAMonth(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data := qaWorkloadData{Month: current}
		if raw := strings.TrimSpace(c.Query("month")); raw != "" {
			if t, err := time.Parse("2006-01", raw); err == nil && t.Format("2006-01") == raw {
				data.Month = raw
			} else {
				data.Unknown = raw
			}
		}
		w, err := repo.LoadQAWorkload(c.Context(), s, data.Month)
		if err != nil {
			return writeRepoErr(c, err)
		}
		m, _ := time.Parse("2006-01", data.Month)
		data.MonthLabel = m.Format("January 2006")
		data.PrevHref = "/qa/workload?month=" + m.AddDate(0, -1, 0).Format("2006-01")
		if data.Month < current {
			data.NextHref = "/qa/workload?month=" + m.AddDate(0, 1, 0).Format("2006-01")
		}
		buildQAWorkloadRows(&data, w)

		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_workload.html", httpx.ViewData{
			Title: "QC workload", Org: org, User: user,
			Nav:  navWithTrail("qa_tools", len(projects), []httpx.NavCrumb{{Label: "QA Tools", Href: "/qa"}, {Label: "QC workload"}}),
			Data: data,
		})
	}
}

func buildQAWorkloadRows(data *qaWorkloadData, w *repo.QAWorkload) {
	issue := func(is repo.QAWorkloadIssue) qaWLIssue {
		return qaWLIssue{Key: "BUG-" + strconv.Itoa(is.Key), Title: is.Title, Project: is.ProjectName,
			Reporter: is.Reporter, Status: is.Status, Href: qaBaseFor(is.ProjectID, is.ProjectSlug) + "/issues/" + strconv.Itoa(is.Key)}
	}
	run := func(r repo.QAWorkloadRun) qaWLRun {
		return qaWLRun{Name: r.Name, Href: qaBaseFor(r.ProjectID, r.ProjectSlug) + "/runs/" + r.RunID.String(), qaWLCounts: wlCounts(r.QAWorkloadCounts)}
	}

	// Per person: their runs grouped by project, in the order the runs
	// were created, and the issues they reported.
	personRuns := map[uuid.UUID][]repo.QAWorkloadRun{}
	for _, r := range w.PersonRuns {
		personRuns[*r.AssigneeID] = append(personRuns[*r.AssigneeID], r)
	}
	reportedBy := map[uuid.UUID][]qaWLIssue{}
	byProject := map[uuid.UUID][]qaWLIssue{}
	for _, is := range w.Issues {
		if is.ReportedBy != nil {
			reportedBy[*is.ReportedBy] = append(reportedBy[*is.ReportedBy], issue(is))
		}
		byProject[is.ProjectID] = append(byProject[is.ProjectID], issue(is))
	}
	data.People = make([]qaWLPersonRow, 0, len(w.People))
	for _, p := range w.People {
		row := qaWLPersonRow{Name: p.Name, Role: p.Role, Projects: p.Projects, Cases: p.CasesCreated,
			Issues: p.IssuesReported, qaWLCounts: wlCounts(p.QAWorkloadCounts),
			Detail: []qaWLRunGroup{}, Reported: reportedBy[p.UserID]}
		if row.Reported == nil {
			row.Reported = []qaWLIssue{}
		}
		group := map[uuid.UUID]int{}
		for _, r := range personRuns[p.UserID] {
			i, ok := group[r.ProjectID]
			if !ok {
				i = len(row.Detail)
				group[r.ProjectID] = i
				row.Detail = append(row.Detail, qaWLRunGroup{Project: r.ProjectName, Href: qaBaseFor(r.ProjectID, r.ProjectSlug)})
			}
			row.Detail[i].Runs = append(row.Detail[i].Runs, run(r))
		}
		data.People = append(data.People, row)
	}

	projectRuns := map[uuid.UUID][]qaWLRun{}
	for _, r := range w.Runs {
		projectRuns[r.ProjectID] = append(projectRuns[r.ProjectID], run(r))
	}
	data.Projects = make([]qaWLProjectRow, 0, len(w.Projects))
	for _, p := range w.Projects {
		row := qaWLProjectRow{Name: p.Name, Href: qaBaseFor(p.ProjectID, p.ProjectSlug), Cases: p.CasesCreated, Runs: p.RunsCreated,
			Issues: p.IssuesCreated, qaWLCounts: wlCounts(p.QAWorkloadCounts),
			RunList: projectRuns[p.ProjectID], Reported: byProject[p.ProjectID]}
		if row.RunList == nil {
			row.RunList = []qaWLRun{}
		}
		if row.Reported == nil {
			row.Reported = []qaWLIssue{}
		}
		data.Projects = append(data.Projects, row)
	}
}
