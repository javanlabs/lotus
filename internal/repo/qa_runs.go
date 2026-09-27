package repo

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

const (
	DistributeNone       = "none"
	DistributeRoundRobin = "round_robin"
	DistributeByGroup    = "by_group"
)

type QARun struct {
	ID        uuid.UUID  `db:"id"`
	OrgID     uuid.UUID  `db:"org_id"`
	PlanID    uuid.UUID  `db:"plan_id"`
	Name      string     `db:"name"`
	Cycle     int        `db:"cycle"`
	RetestOf  *uuid.UUID `db:"retest_of"`
	Notes     string     `db:"notes"`
	ClosedAt  *time.Time `db:"closed_at"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

const qaRunCols = `id, org_id, plan_id, name, cycle, retest_of, notes, closed_at, created_by, created_at`

type QARunInput struct {
	Name         string
	Notes        string
	ScenarioIDs  []uuid.UUID // subset of the plan's; empty = all
	Testers      []uuid.UUID
	Distribution string
}

// runSource is one case to snapshot, in run order.
type runSource struct {
	CaseID       uuid.UUID `db:"case_id"`
	GroupName    string    `db:"group_name"`
	Title        string    `db:"title"`
	Precondition string    `db:"precondition"`
	Steps        QASteps   `db:"steps"`
	Version      int       `db:"version"`
	Assignee     *uuid.UUID
}

func nextQACycle(ctx context.Context, tx *sqlx.Tx, planID uuid.UUID) (int, error) {
	// Lock the plan row so two concurrent run creations get distinct cycles.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM qa_plans WHERE id = $1 FOR UPDATE`, planID); err != nil {
		return 0, err
	}
	var c int
	err := tx.GetContext(ctx, &c, `SELECT COALESCE(max(cycle), 0) + 1 FROM qa_runs WHERE plan_id = $1`, planID)
	return c, err
}

func insertQARun(ctx context.Context, tx *sqlx.Tx, orgID, planID uuid.UUID, name, notes string, retestOf, createdBy *uuid.UUID, items []runSource) (*QARun, error) {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrQAEmptyRun
	}
	cycle, err := nextQACycle(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	var run QARun
	if err := tx.GetContext(ctx, &run, `
		INSERT INTO qa_runs (org_id, plan_id, name, cycle, retest_of, notes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+qaRunCols,
		orgID, planID, name, cycle, retestOf, notes, createdBy); err != nil {
		return nil, err
	}
	for i, it := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO qa_run_items (org_id, run_id, case_id, group_name, position, title, precondition, steps, case_version, assignee_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			orgID, run.ID, it.CaseID, it.GroupName, i+1, it.Title, it.Precondition, it.Steps, it.Version, it.Assignee); err != nil {
			return nil, err
		}
	}
	return &run, nil
}

func validQADistribution(in QARunInput) error {
	switch in.Distribution {
	case "", DistributeNone:
	case DistributeRoundRobin, DistributeByGroup:
		if len(in.Testers) == 0 {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	return nil
}

// qaRunSources is the cases a run of planID over scenarioIDs (empty: all
// the plan's scenarios) would hold, in run order: live cases only, each
// once, grouped by scenario and group.
func qaRunSources(ctx context.Context, tx *sqlx.Tx, planID uuid.UUID, scenarioIDs []uuid.UUID) ([]runSource, error) {
	var planScenarios []struct {
		ID   uuid.UUID `db:"id"`
		Name string    `db:"name"`
	}
	if err := tx.SelectContext(ctx, &planScenarios, `
		SELECT sc.id, sc.name FROM qa_plan_scenarios ps JOIN qa_scenarios sc ON sc.id = ps.scenario_id
		WHERE ps.plan_id = $1 ORDER BY lower(sc.name)`, planID); err != nil {
		return nil, err
	}
	wanted := map[uuid.UUID]bool{}
	for _, id := range scenarioIDs {
		wanted[id] = true
	}
	chosen := planScenarios[:0:0]
	for _, sc := range planScenarios {
		if len(wanted) == 0 || wanted[sc.ID] {
			chosen = append(chosen, sc)
			delete(wanted, sc.ID)
		}
	}
	if len(wanted) > 0 {
		return nil, ErrInvalidInput // asked for a scenario the plan does not cover
	}
	multi := len(chosen) > 1
	seen := map[uuid.UUID]bool{}
	var items []runSource
	for _, sc := range chosen {
		var rows []runSource
		if err := tx.SelectContext(ctx, &rows, `
			SELECT c.id AS case_id, COALESCE(g.name, '') AS group_name,
				c.title, c.precondition, c.steps, c.version
			FROM qa_scenario_items i
			JOIN qa_cases c ON c.id = i.case_id
			LEFT JOIN qa_scenario_groups g ON g.id = i.group_id
			WHERE i.scenario_id = $1 AND c.archived_at IS NULL
			ORDER BY COALESCE(g.position, 0), i.position, c.key`, sc.ID); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if seen[r.CaseID] {
				continue
			}
			seen[r.CaseID] = true
			if multi {
				if r.GroupName == "" {
					r.GroupName = sc.Name
				} else {
					r.GroupName = sc.Name + " / " + r.GroupName
				}
			}
			items = append(items, r)
		}
	}
	return items, nil
}

func CreateQARun(ctx context.Context, s tenant.Scope, planID uuid.UUID, in QARunInput, createdBy *uuid.UUID) (*QARun, error) {
	if err := validQADistribution(in); err != nil {
		return nil, err
	}
	var out *QARun
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		// Under lock: a CloseQAPlan in flight finishes first, and this sees it.
		var closed *time.Time
		if err := tx.GetContext(ctx, &closed, `SELECT closed_at FROM qa_plans WHERE id = $1 FOR UPDATE`, planID); err != nil {
			return err
		}
		if closed != nil {
			return ErrQAClosed
		}
		items, err := qaRunSources(ctx, tx, planID, in.ScenarioIDs)
		if err != nil {
			return err
		}
		distribute(items, in.Testers, in.Distribution)
		run, err := insertQARun(ctx, tx, s.OrgID(), planID, in.Name, in.Notes, nil, createdBy, items)
		out = run
		return err
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return out, nil
}

// QARunPreview is what CreateQARun would do with the same input: how many
// cases, and how many each tester would get.
type QARunPreview struct {
	Cases      int
	PerTester  map[uuid.UUID]int
	Unassigned int
}

// PreviewQARun computes CreateQARun's case list and distribution without
// writing anything. A plan with nothing to run previews as zero cases.
func PreviewQARun(ctx context.Context, s tenant.Scope, planID uuid.UUID, in QARunInput) (QARunPreview, error) {
	out := QARunPreview{PerTester: map[uuid.UUID]int{}}
	if err := validQADistribution(in); err != nil {
		return out, err
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		items, err := qaRunSources(ctx, tx, planID, in.ScenarioIDs)
		if err != nil {
			return err
		}
		distribute(items, in.Testers, in.Distribution)
		out.Cases = len(items)
		for _, it := range items {
			if it.Assignee == nil {
				out.Unassigned++
			} else {
				out.PerTester[*it.Assignee]++
			}
		}
		return nil
	})
	return out, qaWriteErr(err)
}

// distribute sets Assignee on items in place.
func distribute(items []runSource, testers []uuid.UUID, mode string) {
	n := len(testers)
	if n == 0 {
		return
	}
	switch mode {
	case DistributeRoundRobin:
		for i := range items {
			t := testers[i%n]
			items[i].Assignee = &t
		}
	case DistributeByGroup:
		groupIndex := map[string]int{}
		for i := range items {
			g, ok := groupIndex[items[i].GroupName]
			if !ok {
				g = len(groupIndex)
				groupIndex[items[i].GroupName] = g
			}
			t := testers[g%n]
			items[i].Assignee = &t
		}
	}
}

func CreateQARetest(ctx context.Context, s tenant.Scope, runID uuid.UUID, createdBy *uuid.UUID) (*QARun, error) {
	var out *QARun
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var orig QARun
		if err := tx.GetContext(ctx, &orig, `SELECT `+qaRunCols+` FROM qa_runs WHERE id = $1`, runID); err != nil {
			return err
		}
		var closed *time.Time
		if err := tx.GetContext(ctx, &closed, `SELECT closed_at FROM qa_plans WHERE id = $1 FOR UPDATE`, orig.PlanID); err != nil {
			return err
		}
		if closed != nil {
			return ErrQAClosed
		}
		var rows []struct {
			runSource
			Executor *uuid.UUID `db:"executor"`
		}
		if err := tx.SelectContext(ctx, &rows, `
			SELECT c.id AS case_id, i.group_name, c.title, c.precondition, c.steps, c.version,
				COALESCE(i.executed_by, i.assignee_id) AS executor
			FROM qa_run_items i JOIN qa_cases c ON c.id = i.case_id
			WHERE i.run_id = $1 AND i.status IN ('failed', 'blocked')
			ORDER BY i.position`, runID); err != nil {
			return err
		}
		items := make([]runSource, len(rows))
		for i, r := range rows {
			items[i] = r.runSource
			items[i].Assignee = r.Executor
		}
		run, err := insertQARun(ctx, tx, s.OrgID(), orig.PlanID, qaRetestName(orig.Name), "", &runID, createdBy, items)
		out = run
		return err
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return out, nil
}

func GetQARun(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QARun, error) {
	var out QARun
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaRunCols+` FROM qa_runs WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQARuns(ctx context.Context, s tenant.Scope, planID uuid.UUID) ([]QARun, error) {
	var out []QARun
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+qaRunCols+` FROM qa_runs WHERE plan_id = $1 ORDER BY cycle`, planID)
	})
	return out, err
}

func CloseQARun(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_runs SET closed_at = now() WHERE id = $1 AND closed_at IS NULL`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

const qaRetestSuffix = " — retest"

// qaRetestName names a retest after the run it retests: "Cycle 1" and
// "Cycle 1 — retest" both give "Cycle 1 — retest", so a chain of retests
// does not grow its name, and a long name is cut to fit MaxQANameLength.
func qaRetestName(name string) string {
	base := []rune(strings.TrimSuffix(name, qaRetestSuffix))
	room := MaxQANameLength - utf8.RuneCountInString(qaRetestSuffix)
	if len(base) > room {
		base = append(base[:room-1], '…')
	}
	return string(base) + qaRetestSuffix
}
