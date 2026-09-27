package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

type QAPlan struct {
	ID          uuid.UUID  `db:"id"`
	OrgID       uuid.UUID  `db:"org_id"`
	ProjectID   uuid.UUID  `db:"project_id"`
	Name        string     `db:"name"`
	Phase       string     `db:"phase"`
	Environment string     `db:"environment"`
	Build       string     `db:"build"`
	StartsOn    *time.Time `db:"starts_on"`
	EndsOn      *time.Time `db:"ends_on"`
	ClosedAt    *time.Time `db:"closed_at"`
	CreatedBy   *uuid.UUID `db:"created_by"`
	CreatedAt   time.Time  `db:"created_at"`
}

const qaPlanCols = `id, org_id, project_id, name, phase, environment, build, starts_on, ends_on, closed_at, created_by, created_at`

type QAPlanInput struct {
	Name        string
	Phase       string
	Environment string
	Build       string
	StartsOn    *time.Time
	EndsOn      *time.Time
	ScenarioIDs []uuid.UUID
}

func (in QAPlanInput) normalized() (QAPlanInput, error) {
	n, err := normalizeQAName(in.Name, MaxQANameLength)
	if err != nil {
		return in, err
	}
	if !validQAPhase(in.Phase) {
		return in, ErrInvalidInput
	}
	if in.StartsOn != nil && in.EndsOn != nil && in.EndsOn.Before(*in.StartsOn) {
		return in, ErrInvalidInput
	}
	in.Name = n
	in.Environment = strings.TrimSpace(in.Environment)
	in.Build = strings.TrimSpace(in.Build)
	return in, nil
}

// setQAPlanScenarios replaces the plan's scenario set. Scenarios of another
// project are refused.
func setQAPlanScenarios(ctx context.Context, tx *sqlx.Tx, orgID, planID uuid.UUID, ids []uuid.UUID) error {
	var foreign int
	if err := tx.GetContext(ctx, &foreign, `
		SELECT count(*) FROM qa_scenarios sc, qa_plans p
		WHERE p.id = $1 AND sc.id = ANY($2) AND sc.project_id <> p.project_id`, planID, ids); err != nil {
		return err
	}
	if foreign > 0 {
		return ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qa_plan_scenarios WHERE plan_id = $1`, planID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO qa_plan_scenarios (org_id, plan_id, scenario_id)
		SELECT $1, $2, id FROM unnest($3::uuid[]) AS id
		ON CONFLICT DO NOTHING`, orgID, planID, ids)
	return err
}

func CreateQAPlan(ctx context.Context, s tenant.Scope, projectID uuid.UUID, in QAPlanInput, createdBy *uuid.UUID) (*QAPlan, error) {
	in, err := in.normalized()
	if err != nil {
		return nil, err
	}
	var out QAPlan
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out, `
			INSERT INTO qa_plans (org_id, project_id, name, phase, environment, build, starts_on, ends_on, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+qaPlanCols,
			s.OrgID(), projectID, in.Name, in.Phase, in.Environment, in.Build, in.StartsOn, in.EndsOn, createdBy); err != nil {
			return err
		}
		return setQAPlanScenarios(ctx, tx, s.OrgID(), out.ID, in.ScenarioIDs)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func GetQAPlan(ctx context.Context, s tenant.Scope, id uuid.UUID) (*QAPlan, error) {
	var out QAPlan
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+qaPlanCols+` FROM qa_plans WHERE id = $1`, id)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

func ListQAPlans(ctx context.Context, s tenant.Scope, projectID uuid.UUID) ([]QAPlan, error) {
	var out []QAPlan
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaPlanCols+` FROM qa_plans WHERE project_id = $1
			ORDER BY closed_at IS NOT NULL, created_at DESC`, projectID)
	})
	return out, err
}

func UpdateQAPlan(ctx context.Context, s tenant.Scope, id uuid.UUID, in QAPlanInput) error {
	in, err := in.normalized()
	if err != nil {
		return err
	}
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_plans SET name = $2, phase = $3, environment = $4, build = $5, starts_on = $6, ends_on = $7
			WHERE id = $1`, id, in.Name, in.Phase, in.Environment, in.Build, in.StartsOn, in.EndsOn)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(res); err != nil {
			return err
		}
		return setQAPlanScenarios(ctx, tx, s.OrgID(), id, in.ScenarioIDs)
	})
	return qaWriteErr(err)
}

func ListQAPlanScenarioIDs(ctx context.Context, s tenant.Scope, planID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &ids, `
			SELECT ps.scenario_id FROM qa_plan_scenarios ps
			JOIN qa_scenarios sc ON sc.id = ps.scenario_id
			WHERE ps.plan_id = $1 ORDER BY lower(sc.name)`, planID)
	})
	return ids, err
}

func DuplicateQAPlan(ctx context.Context, s tenant.Scope, planID uuid.UUID, name string, createdBy *uuid.UUID) (*QAPlan, error) {
	src, err := GetQAPlan(ctx, s, planID)
	if err != nil {
		return nil, err
	}
	ids, err := ListQAPlanScenarioIDs(ctx, s, planID)
	if err != nil {
		return nil, err
	}
	return CreateQAPlan(ctx, s, src.ProjectID, QAPlanInput{
		Name: name, Phase: src.Phase, Environment: src.Environment, Build: src.Build, ScenarioIDs: ids,
	}, createdBy)
}

func CloseQAPlan(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE qa_plans SET closed_at = now() WHERE id = $1 AND closed_at IS NULL`, id)
		if err != nil {
			return err
		}
		if err := checkRowsAffected(res); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE qa_runs SET closed_at = now() WHERE plan_id = $1 AND closed_at IS NULL`, id)
		return err
	})
	return qaWriteErr(err)
}
