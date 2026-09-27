package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// A share link opens one plan's read-only report to whoever holds it, with
// no login (/s/qa/:token). Only the token's sha256 is stored; a link lasts
// QAShareLinkTTL unless revoked first.

const QAShareLinkTTL = 30 * 24 * time.Hour

type QAShareLink struct {
	ID        uuid.UUID  `db:"id"`
	PlanID    uuid.UUID  `db:"plan_id"`
	ExpiresAt time.Time  `db:"expires_at"`
	RevokedAt *time.Time `db:"revoked_at"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

const qaShareLinkCols = `id, plan_id, expires_at, revoked_at, created_by, created_at`

func CreateQAShareLink(ctx context.Context, s tenant.Scope, planID uuid.UUID, tokenHash string, by *uuid.UUID) (*QAShareLink, error) {
	var out QAShareLink
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_share_links (org_id, plan_id, token_hash, expires_at, created_by)
			VALUES ($1, $2, $3, now() + $4 * interval '1 second', $5) RETURNING `+qaShareLinkCols,
			s.OrgID(), planID, tokenHash, int64(QAShareLinkTTL/time.Second), by)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

// QAShareLinkByHash is the live link with this token hash: ErrNotFound when
// there is none, or it was revoked, or it has expired.
func QAShareLinkByHash(ctx context.Context, s tenant.Scope, tokenHash string) (*QAShareLink, error) {
	var out QAShareLink
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT `+qaShareLinkCols+` FROM qa_share_links
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`, tokenHash)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

// ListQAShareLinks is planID's live links, newest first.
func ListQAShareLinks(ctx context.Context, s tenant.Scope, planID uuid.UUID) ([]QAShareLink, error) {
	var out []QAShareLink
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+qaShareLinkCols+` FROM qa_share_links
			WHERE plan_id = $1 AND revoked_at IS NULL AND expires_at > now()
			ORDER BY created_at DESC, id`, planID)
	})
	return out, err
}

// RevokeQAShareLink ends a live link of planID at once.
func RevokeQAShareLink(ctx context.Context, s tenant.Scope, planID, id uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE qa_share_links SET revoked_at = now()
			WHERE id = $1 AND plan_id = $2 AND revoked_at IS NULL`, id, planID)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

// QAPlanScenarioProgress is, for each scenario of planID, the latest status
// (highest cycle) of each of its cases that the plan's runs hold.
func QAPlanScenarioProgress(ctx context.Context, s tenant.Scope, planID uuid.UUID) (map[uuid.UUID]QAProgress, error) {
	var rows []struct {
		ScenarioID uuid.UUID `db:"scenario_id"`
		Status     string    `db:"status"`
		N          int       `db:"n"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT si.scenario_id, latest.status, count(*) AS n
			FROM (
				SELECT DISTINCT ON (i.case_id) i.case_id, i.status
				FROM qa_run_items i JOIN qa_runs r ON r.id = i.run_id
				WHERE r.plan_id = $1
				ORDER BY i.case_id, r.cycle DESC
			) latest
			JOIN qa_scenario_items si ON si.case_id = latest.case_id
			JOIN qa_plan_scenarios ps ON ps.scenario_id = si.scenario_id AND ps.plan_id = $1
			GROUP BY si.scenario_id, latest.status`, planID)
	})
	out := map[uuid.UUID]QAProgress{}
	for _, r := range rows {
		p := out[r.ScenarioID]
		p.add(r.Status, r.N)
		out[r.ScenarioID] = p
	}
	return out, err
}
