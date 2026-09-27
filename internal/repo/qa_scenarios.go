package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

type QAScenario struct {
	ID          uuid.UUID  `db:"id"`
	OrgID       uuid.UUID  `db:"org_id"`
	ProjectID   uuid.UUID  `db:"project_id"`
	Name        string     `db:"name"`
	Phase       string     `db:"phase"`
	Description string     `db:"description"`
	ArchivedAt  *time.Time `db:"archived_at"`
	CreatedBy   *uuid.UUID `db:"created_by"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
}

const qaScenarioCols = `id, org_id, project_id, name, phase, description, archived_at, created_by, created_at, updated_at`

type QAScenarioGroup struct {
	ID         uuid.UUID `db:"id"`
	OrgID      uuid.UUID `db:"org_id"`
	ScenarioID uuid.UUID `db:"scenario_id"`
	Name       string    `db:"name"`
	Position   int       `db:"position"`
}

type QAScenarioEntry struct {
	ItemID        uuid.UUID  `db:"item_id"`
	CaseID        uuid.UUID  `db:"case_id"`
	CaseKey       int        `db:"case_key"`
	Title         string     `db:"title"`
	CaseArchived  bool       `db:"case_archived"`
	GroupID       *uuid.UUID `db:"group_id"`
	GroupName     string     `db:"group_name"`
	GroupPosition int        `db:"group_position"`
	Position      int        `db:"position"`
}

func CreateQAScenario(ctx context.Context, s tenant.Scope, projectID uuid.UUID, name, phase, description string, createdBy *uuid.UUID) (*QAScenario, error) {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return nil, err
	}
	if !validQAPhase(phase) {
		return nil, ErrInvalidInput
	}
	var out QAScenario
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_scenarios (org_id, project_id, name, phase, description, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+qaScenarioCols,
			s.OrgID(), projectID, name, phase, strings.TrimSpace(description), createdBy)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQAScenario(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QAScenario, error) {
	var out QAScenario
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaScenarioCols+` FROM qa_scenarios WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQAScenarios(ctx context.Context, s tenant.Scope, projectID uuid.UUID, includeArchived bool) ([]QAScenario, error) {
	var out []QAScenario
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaScenarioCols+` FROM qa_scenarios
			WHERE project_id = $1 AND ($2 OR archived_at IS NULL)
			ORDER BY lower(name)`, projectID, includeArchived)
	})
	return out, err
}

func UpdateQAScenario(ctx context.Context, s tenant.Scope, id uuid.UUID, name, phase, description string) error {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return err
	}
	if !validQAPhase(phase) {
		return ErrInvalidInput
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_scenarios SET name = $2, phase = $3, description = $4, updated_at = now() WHERE id = $1`,
			id, name, phase, strings.TrimSpace(description))
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

func ArchiveQAScenario(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_scenarios SET archived_at = now(), updated_at = now() WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

func CreateQAScenarioGroup(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, name string) (*QAScenarioGroup, error) {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return nil, err
	}
	var out QAScenarioGroup
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_scenario_groups (org_id, scenario_id, name, position)
			SELECT $1, $2, $3, COALESCE(max(position), 0) + 1 FROM qa_scenario_groups WHERE scenario_id = $2
			RETURNING id, org_id, scenario_id, name, position`, s.OrgID(), scenarioID, name)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func AddQAScenarioCases(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, groupID *uuid.UUID, caseIDs []uuid.UUID) (int, error) {
	var added int64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var foreign int
		if err := tx.GetContext(ctx, &foreign, `
			SELECT count(*) FROM qa_cases c, qa_scenarios sc
			WHERE sc.id = $1 AND c.id = ANY($2) AND c.project_id <> sc.project_id`, scenarioID, caseIDs); err != nil {
			return err
		}
		if foreign > 0 {
			return ErrInvalidInput
		}
		if groupID != nil {
			var ok bool
			if err := tx.GetContext(ctx, &ok, `
				SELECT EXISTS (SELECT 1 FROM qa_scenario_groups WHERE id = $1 AND scenario_id = $2)`,
				*groupID, scenarioID); err != nil {
				return err
			}
			if !ok {
				return ErrInvalidInput // a group of another scenario
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO qa_scenario_items (org_id, scenario_id, group_id, case_id, position)
			SELECT $1, $2, $3, ids.id,
				(SELECT COALESCE(max(position), 0) FROM qa_scenario_items WHERE scenario_id = $2) + ids.ord
			FROM unnest($4::uuid[]) WITH ORDINALITY AS ids(id, ord)
			ON CONFLICT (scenario_id, case_id) DO NOTHING`, s.OrgID(), scenarioID, groupID, caseIDs)
		if err != nil {
			return err
		}
		added, err = res.RowsAffected()
		return err
	})
	return int(added), qaWriteErr(err)
}

func RemoveQAScenarioCases(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, caseIDs []uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM qa_scenario_items WHERE scenario_id = $1 AND case_id = ANY($2)`, scenarioID, caseIDs)
		return err
	})
}

func ReorderQAScenario(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, itemIDs []uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE qa_scenario_items i SET position = o.ord
			FROM unnest($2::uuid[]) WITH ORDINALITY AS o(id, ord)
			WHERE i.id = o.id AND i.scenario_id = $1`, scenarioID, itemIDs)
		return err
	})
}

func ListQAScenarioEntries(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID) ([]QAScenarioEntry, error) {
	var out []QAScenarioEntry
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT i.id AS item_id, c.id AS case_id, c.key AS case_key, c.title,
				c.archived_at IS NOT NULL AS case_archived,
				i.group_id, COALESCE(g.name, '') AS group_name, COALESCE(g.position, 0) AS group_position,
				i.position
			FROM qa_scenario_items i
			JOIN qa_cases c ON c.id = i.case_id
			LEFT JOIN qa_scenario_groups g ON g.id = i.group_id
			WHERE i.scenario_id = $1
			ORDER BY COALESCE(g.position, 0), i.position, c.key`, scenarioID)
	})
	return out, err
}

// ListQAScenarioGroups returns the scenario's groups by position, including
// groups that hold no cases yet.
func ListQAScenarioGroups(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID) ([]QAScenarioGroup, error) {
	var out []QAScenarioGroup
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, scenario_id, name, position FROM qa_scenario_groups
			WHERE scenario_id = $1 ORDER BY position, lower(name)`, scenarioID)
	})
	return out, err
}

// RenameQAScenarioGroup renames a group of scenarioID. A group of another
// scenario is ErrNotFound.
func RenameQAScenarioGroup(ctx context.Context, s tenant.Scope, scenarioID, groupID uuid.UUID, name string) error {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return err
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_scenario_groups SET name = $3 WHERE id = $2 AND scenario_id = $1`,
			scenarioID, groupID, name)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
	return qaWriteErr(err)
}

// ReorderQAScenarioGroups gives the listed groups positions 1..n in that
// order. Ids of other scenarios' groups are ignored.
func ReorderQAScenarioGroups(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, groupIDs []uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE qa_scenario_groups g SET position = o.ord
			FROM unnest($2::uuid[]) WITH ORDINALITY AS o(id, ord)
			WHERE g.id = o.id AND g.scenario_id = $1`, scenarioID, groupIDs)
		return err
	})
}

// SetQAScenarioItemsGroup moves items of scenarioID into groupID (nil: no
// group). Items of other scenarios are skipped; a group of another scenario
// is ErrNotFound. It returns how many items moved.
func SetQAScenarioItemsGroup(ctx context.Context, s tenant.Scope, scenarioID uuid.UUID, itemIDs []uuid.UUID, groupID *uuid.UUID) (int, error) {
	var n int64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if groupID != nil {
			var ok bool
			if err := tx.GetContext(ctx, &ok, `
				SELECT EXISTS (SELECT 1 FROM qa_scenario_groups WHERE id = $1 AND scenario_id = $2)`, *groupID, scenarioID); err != nil {
				return err
			}
			if !ok {
				return ErrNotFound
			}
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_scenario_items SET group_id = $3 WHERE scenario_id = $1 AND id = ANY($2)`, scenarioID, itemIDs, groupID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), qaWriteErr(err)
}

// CreateQAScenarioFromRun saves a run's case set as a new scenario of the
// same project: the cases in run order, one group per group name the run
// used ("" stays ungrouped). Cases archived since the run are left out.
func CreateQAScenarioFromRun(ctx context.Context, s tenant.Scope, runID uuid.UUID, name, phase string, createdBy *uuid.UUID) (*QAScenario, error) {
	name, err := normalizeQAName(name, MaxQANameLength)
	if err != nil {
		return nil, err
	}
	if !validQAPhase(phase) {
		return nil, ErrInvalidInput
	}
	var out QAScenario
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		var projectID uuid.UUID
		if err := tx.GetContext(ctx, &projectID, `
			SELECT p.project_id FROM qa_runs r JOIN qa_plans p ON p.id = r.plan_id WHERE r.id = $1`, runID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &out, `
			INSERT INTO qa_scenarios (org_id, project_id, name, phase, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+qaScenarioCols, s.OrgID(), projectID, name, phase, createdBy); err != nil {
			return err
		}
		var items []struct {
			CaseID    uuid.UUID `db:"case_id"`
			GroupName string    `db:"group_name"`
		}
		if err := tx.SelectContext(ctx, &items, `
			SELECT i.case_id, i.group_name FROM qa_run_items i JOIN qa_cases c ON c.id = i.case_id
			WHERE i.run_id = $1 AND c.archived_at IS NULL ORDER BY i.position`, runID); err != nil {
			return err
		}
		groups := map[string]*uuid.UUID{"": nil}
		for pos, it := range items {
			g, ok := groups[it.GroupName]
			if !ok {
				var id uuid.UUID
				if err := tx.GetContext(ctx, &id, `
					INSERT INTO qa_scenario_groups (org_id, scenario_id, name, position) VALUES ($1, $2, $3, $4) RETURNING id`,
					s.OrgID(), out.ID, truncateQAName(it.GroupName), len(groups)); err != nil {
					return err
				}
				g = &id
				groups[it.GroupName] = g
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO qa_scenario_items (org_id, scenario_id, group_id, case_id, position) VALUES ($1, $2, $3, $4, $5)`,
				s.OrgID(), out.ID, g, it.CaseID, pos+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}
