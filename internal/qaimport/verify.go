package qaimport

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/legacyqa"
	"github.com/uyab/lotus/internal/tenant"
)

type Mismatch struct {
	LegacyProjectID uuid.UUID
	Project         string
	Field           string
	Legacy, Lotus   int
}

// legacyCounts computes, from the legacy side, what an import of this
// project must have produced — the same inclusion and mapping rules the
// importer applies, so the two sides are comparable number for number.
func legacyCounts(ctx context.Context, r *legacyqa.Reader, pid uuid.UUID) (repo.QACounts, error) {
	out := repo.QACounts{ItemStatus: map[string]int{}, IssueStatus: map[string]int{}}
	cases, err := r.Cases(ctx, pid)
	if err != nil {
		return out, err
	}
	out.Cases = len(cases)
	for _, c := range cases {
		if c.DeletedAt != nil || c.Archived {
			out.ArchivedCases++
		}
	}
	runs, err := r.Runs(ctx, pid)
	if err != nil {
		return out, err
	}
	out.Runs = len(runs)
	details, err := r.Details(ctx, pid)
	if err != nil {
		return out, err
	}
	for _, d := range details {
		out.ItemStatus[MapItemStatus(d.Status)]++
		out.Items++
	}
	results, err := r.Results(ctx, pid)
	if err != nil {
		return out, err
	}
	for _, res := range results {
		if _, ok := MapResultStatus(res.Status); ok {
			out.Results++
		}
	}
	issues, err := r.Issues(ctx, pid)
	if err != nil {
		return out, err
	}
	for _, is := range issues {
		out.IssueStatus[MapIssueStatus(is.Status)]++
		out.Issues++
	}
	comments, err := r.Comments(ctx, pid)
	if err != nil {
		return out, err
	}
	logs, err := r.IssueLogs(ctx, pid)
	if err != nil {
		return out, err
	}
	out.IssueEvents = len(comments)
	for _, l := range logs {
		if MapIssueStatus(l.From) != MapIssueStatus(l.To) {
			out.IssueEvents++
		}
	}
	return out, nil
}

func Verify(ctx context.Context, db *sqlx.DB, legacy *legacyqa.Reader, orgID uuid.UUID) ([]Mismatch, error) {
	s := tenant.FromRun(db, orgID)
	done, err := repo.ListQALegacyIDs(ctx, s, "project_done")
	if err != nil {
		return nil, err
	}
	projects, err := legacy.Projects(ctx)
	if err != nil {
		return nil, err
	}
	titles := map[uuid.UUID]string{}
	for _, p := range projects {
		titles[p.ID] = p.Title
	}
	var out []Mismatch
	// Organization-wide: every project holding QA data must be one the
	// import finished. Run verify right after importing, before people start
	// creating QA data in new projects.
	withData, err := repo.CountProjectsWithQAData(ctx, s)
	if err != nil {
		return nil, err
	}
	if withData != len(done) {
		out = append(out, Mismatch{Project: "(organization)", Field: "projects_with_qa_data", Legacy: len(done), Lotus: withData})
	}
	for legacyID, projectID := range done {
		want, err := legacyCounts(ctx, legacy, legacyID)
		if err != nil {
			return nil, err
		}
		got, err := repo.QACountsForProject(ctx, s, projectID)
		if err != nil {
			return nil, err
		}
		add := func(field string, l, n int) {
			if l != n {
				out = append(out, Mismatch{LegacyProjectID: legacyID, Project: titles[legacyID], Field: field, Legacy: l, Lotus: n})
			}
		}
		add("cases", want.Cases, got.Cases)
		add("archived_cases", want.ArchivedCases, got.ArchivedCases)
		add("runs", want.Runs, got.Runs)
		add("items", want.Items, got.Items)
		add("results", want.Results, got.Results)
		add("issues", want.Issues, got.Issues)
		add("issue_events", want.IssueEvents, got.IssueEvents)
		for _, st := range []string{"untested", "passed", "failed", "blocked", "skipped"} {
			add("items."+st, want.ItemStatus[st], got.ItemStatus[st])
		}
		for _, st := range repo.QAIssueStatuses {
			add("issues."+st, want.IssueStatus[st], got.IssueStatus[st])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Field < out[j].Field
	})
	return out, nil
}
