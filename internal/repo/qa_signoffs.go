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

// A sign-off is a client's (or an admin's) verdict on a plan, for the whole
// plan or for one of its scenarios. Sign-offs are never edited: a new one
// supersedes the last for the same scope, and the history stays.

// QASignoffDecisions in the order the form offers them.
var QASignoffDecisions = []string{"accepted", "accepted_with_notes", "rejected"}

// MaxQASignoffNote bounds a sign-off note, in runes.
const MaxQASignoffNote = 4000

type QASignoff struct {
	ID         uuid.UUID  `db:"id"`
	PlanID     uuid.UUID  `db:"plan_id"`
	ScenarioID *uuid.UUID `db:"scenario_id"` // nil: the whole plan
	Decision   string     `db:"decision"`
	Note       string     `db:"note"`
	SignedBy   *uuid.UUID `db:"signed_by"`
	SignedAt   time.Time  `db:"signed_at"`
}

const qaSignoffCols = `id, plan_id, scenario_id, decision, note, signed_by, signed_at`

type QASignoffInput struct {
	ScenarioID *uuid.UUID
	Decision   string
	Note       string
}

func (in QASignoffInput) normalized() (QASignoffInput, error) {
	in.Note = strings.TrimSpace(in.Note)
	valid := false
	for _, d := range QASignoffDecisions {
		valid = valid || d == in.Decision
	}
	switch {
	case !valid, utf8.RuneCountInString(in.Note) > MaxQASignoffNote:
		return in, ErrInvalidInput
	case in.Decision != "accepted" && in.Note == "":
		// Anything short of a plain accept has to say what is wrong.
		return in, ErrInvalidInput
	}
	return in, nil
}

// RecordQASignoff signs off planID, or one of its scenarios.
func RecordQASignoff(ctx context.Context, s tenant.Scope, planID uuid.UUID, in QASignoffInput, by *uuid.UUID) (*QASignoff, error) {
	in, err := in.normalized()
	if err != nil {
		return nil, err
	}
	var out QASignoff
	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
		if in.ScenarioID != nil {
			var n int
			if err := tx.GetContext(ctx, &n, `
				SELECT count(*) FROM qa_plan_scenarios WHERE plan_id = $1 AND scenario_id = $2`, planID, *in.ScenarioID); err != nil {
				return err
			}
			if n == 0 {
				return ErrInvalidInput
			}
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO qa_signoffs (org_id, plan_id, scenario_id, decision, note, signed_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+qaSignoffCols,
			s.OrgID(), planID, in.ScenarioID, in.Decision, in.Note, by)
	})
	if err != nil {
		return nil, qaWriteErr(err)
	}
	return &out, nil
}

// ListLatestQASignoffs is the standing sign-off per scope of planID: the
// whole plan first, then each signed scenario.
func ListLatestQASignoffs(ctx context.Context, s tenant.Scope, planID uuid.UUID) ([]QASignoff, error) {
	var out []QASignoff
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT DISTINCT ON (scenario_id) `+qaSignoffCols+` FROM qa_signoffs
			WHERE plan_id = $1
			ORDER BY scenario_id NULLS FIRST, signed_at DESC, id DESC`, planID)
	})
	return out, err
}
