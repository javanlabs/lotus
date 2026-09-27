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
