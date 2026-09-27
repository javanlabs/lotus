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
// are archived (CreateQARun leaves archived cases out of a new run).
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

// QAPlanProgress is, per plan, where each of its cases stands now: the
// status of the case's item in the latest cycle that holds it. So a retest
// that passes a failed case moves the plan forward, and a case never run
// yet does not appear. Plans with no runs map to a zero QAProgress.
func QAPlanProgress(ctx context.Context, s tenant.Scope, planIDs []uuid.UUID) (map[uuid.UUID]QAProgress, error) {
	var rows []struct {
		PlanID uuid.UUID `db:"plan_id"`
		Status string    `db:"status"`
		N      int       `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT plan_id, status, count(*) AS n FROM (
				SELECT DISTINCT ON (r.plan_id, i.case_id) r.plan_id, i.status
				FROM qa_run_items i JOIN qa_runs r ON r.id = i.run_id
				WHERE r.plan_id = ANY($1)
				ORDER BY r.plan_id, i.case_id, r.cycle DESC
			) latest GROUP BY plan_id, status`, planIDs)
	})
	out := make(map[uuid.UUID]QAProgress, len(planIDs))
	for _, r := range rows {
		p := out[r.PlanID]
		p.add(r.Status, r.N)
		out[r.PlanID] = p
	}
	return out, err
}

// QARunProgressFor is QARunProgress's total for several runs at once.
func QARunProgressFor(ctx context.Context, s tenant.Scope, runIDs []uuid.UUID) (map[uuid.UUID]QAProgress, error) {
	var rows []struct {
		RunID  uuid.UUID `db:"run_id"`
		Status string    `db:"status"`
		N      int       `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT run_id, status, count(*) AS n FROM qa_run_items
			WHERE run_id = ANY($1) GROUP BY run_id, status`, runIDs)
	})
	out := make(map[uuid.UUID]QAProgress, len(runIDs))
	for _, r := range rows {
		p := out[r.RunID]
		p.add(r.Status, r.N)
		out[r.RunID] = p
	}
	return out, err
}

// QAPlanRunCounts is how many runs each plan has.
func QAPlanRunCounts(ctx context.Context, s tenant.Scope, planIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	var rows []struct {
		PlanID uuid.UUID `db:"plan_id"`
		N      int       `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT plan_id, count(*) AS n FROM qa_runs WHERE plan_id = ANY($1) GROUP BY plan_id`, planIDs)
	})
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		out[r.PlanID] = r.N
	}
	return out, err
}

// QACaseRef is what a run shows about the live case behind an item: its
// key, and its current version (newer than the item's case_version means
// the case changed after the run was created).
type QACaseRef struct {
	Key     int `db:"key"`
	Version int `db:"version"`
}

func QACaseRefs(ctx context.Context, s tenant.Scope, caseIDs []uuid.UUID) (map[uuid.UUID]QACaseRef, error) {
	var rows []struct {
		ID uuid.UUID `db:"id"`
		QACaseRef
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `SELECT id, key, version FROM qa_cases WHERE id = ANY($1)`, caseIDs)
	})
	out := make(map[uuid.UUID]QACaseRef, len(rows))
	for _, r := range rows {
		out[r.ID] = r.QACaseRef
	}
	return out, err
}
