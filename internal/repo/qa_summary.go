package repo

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// QAOpenIssueStatuses are the statuses an issue still needs work in. "fixed"
// counts: it is waiting for QA to confirm the fix.
var QAOpenIssueStatuses = []string{"new", "open", "fixed", "reopened"}

// QAProjectSummary is one live project's QA counts, for the /qa overview and
// a project's QA landing page.
type QAProjectSummary struct {
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
	Cases       int       `db:"cases"`
	Scenarios   int       `db:"scenarios"`
	OpenPlans   int       `db:"open_plans"`
	OpenIssues  int       `db:"open_issues"`
}

// ListQAProjectSummaries returns every live project, by name, with its QA
// counts: live cases, live scenarios, open plans and open issues. projectIDs
// nil means every project; a guest passes the projects it is listed on, and
// an empty list returns nothing.
func ListQAProjectSummaries(ctx context.Context, s tenant.Scope, projectIDs []uuid.UUID) ([]QAProjectSummary, error) {
	out := []QAProjectSummary{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT p.id AS project_id, p.name AS project_name,
				(SELECT count(*) FROM qa_cases c WHERE c.project_id = p.id AND c.archived_at IS NULL) AS cases,
				(SELECT count(*) FROM qa_scenarios sc WHERE sc.project_id = p.id AND sc.archived_at IS NULL) AS scenarios,
				(SELECT count(*) FROM qa_plans pl WHERE pl.project_id = p.id AND pl.closed_at IS NULL) AS open_plans,
				(SELECT count(*) FROM qa_issues i WHERE i.project_id = p.id AND i.status = ANY($2)) AS open_issues
			FROM projects p
			WHERE p.archived_at IS NULL AND ($1::uuid[] IS NULL OR p.id = ANY($1))
			ORDER BY lower(p.name), p.id`, projectIDs, QAOpenIssueStatuses)
	})
	return out, err
}

// QASummaryForProject is ListQAProjectSummaries for one live project.
func QASummaryForProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID) (QAProjectSummary, error) {
	rows, err := ListQAProjectSummaries(ctx, s, []uuid.UUID{projectID})
	if err != nil {
		return QAProjectSummary{}, err
	}
	if len(rows) == 0 {
		return QAProjectSummary{}, ErrNotFound
	}
	return rows[0], nil
}

// QAScenarioCount is how many cases a scenario holds, and how many of those
// are archived (they still count: runs created from it will include them).
type QAScenarioCount struct {
	Cases    int `db:"cases"`
	Archived int `db:"archived"`
}

func QAScenarioCaseCounts(ctx context.Context, s tenant.Scope, projectID uuid.UUID) (map[uuid.UUID]QAScenarioCount, error) {
	var rows []struct {
		ScenarioID uuid.UUID `db:"scenario_id"`
		QAScenarioCount
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT i.scenario_id, count(*) AS cases, count(c.archived_at) AS archived
			FROM qa_scenario_items i
			JOIN qa_scenarios sc ON sc.id = i.scenario_id
			JOIN qa_cases c ON c.id = i.case_id
			WHERE sc.project_id = $1
			GROUP BY i.scenario_id`, projectID)
	})
	out := make(map[uuid.UUID]QAScenarioCount, len(rows))
	for _, r := range rows {
		out[r.ScenarioID] = r.QAScenarioCount
	}
	return out, err
}
