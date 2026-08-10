package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// TrivyFindingNote is a human (or AI-suggested, task 3.4's ai_generated
// flag — not populated by anything yet, schema-ready for a future task)
// annotation on one CVE within one binding, e.g. "false positive, doesn't
// apply to our build config."
type TrivyFindingNote struct {
	ID           uuid.UUID  `db:"id"`
	OrgID        uuid.UUID  `db:"org_id"`
	BindingID    uuid.UUID  `db:"binding_id"`
	CVEID        string     `db:"cve_id"`
	Package      string     `db:"package"`
	Status       string     `db:"status"`
	Comment      string     `db:"comment"`
	AIGenerated  bool       `db:"ai_generated"`
	AuthorUserID *uuid.UUID `db:"author_user_id"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

type NewTrivyFindingNote struct {
	BindingID    uuid.UUID
	CVEID        string
	Package      string
	Status       string
	Comment      string
	AuthorUserID *uuid.UUID
}

// UpsertTrivyFindingNote creates or replaces the note for (binding_id,
// cve_id, package) — the same CVE+package re-annotated is an edit, not a
// second row, matching the table's own UNIQUE constraint.
func UpsertTrivyFindingNote(ctx context.Context, s tenant.Scope, in NewTrivyFindingNote) (uuid.UUID, error) {
	if in.Status == "" {
		in.Status = "false_positive"
	}
	var id uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &id, `
			INSERT INTO trivy_finding_notes (org_id, binding_id, cve_id, package, status, comment, author_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (binding_id, cve_id, package) DO UPDATE SET
				status = EXCLUDED.status, comment = EXCLUDED.comment,
				author_user_id = EXCLUDED.author_user_id, updated_at = now()
			RETURNING id`,
			s.OrgID(), in.BindingID, in.CVEID, in.Package, in.Status, in.Comment, in.AuthorUserID)
	})
	return id, err
}

func ListTrivyFindingNotes(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]TrivyFindingNote, error) {
	var out []TrivyFindingNote
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, binding_id, cve_id, package, status, comment, ai_generated,
			       author_user_id, created_at, updated_at
			FROM trivy_finding_notes WHERE binding_id = $1 ORDER BY created_at DESC`, bindingID)
	})
	return out, err
}

func DeleteTrivyFindingNote(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM trivy_finding_notes WHERE id = $1`, id)
		return err
	})
}
