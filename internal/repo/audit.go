package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

type AuditEvent struct {
	ID          uuid.UUID       `db:"id"`
	OrgID       uuid.UUID       `db:"org_id"`
	ActorUserID *uuid.UUID      `db:"actor_user_id"`
	Action      string          `db:"action"`
	TargetKind  string          `db:"target_kind"`
	TargetID    *uuid.UUID      `db:"target_id"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
}

// RecordAuditEvent appends an audit_events row. Task 2.2 is what first
// needs this (a base_url change on a connection must always leave a
// trail), but the table and this function are generic — any future
// sensitive mutation can call it the same way.
func RecordAuditEvent(ctx context.Context, s tenant.Scope, actorUserID *uuid.UUID, action, targetKind string, targetID *uuid.UUID, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (org_id, actor_user_id, action, target_kind, target_id, metadata)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			s.OrgID(), actorUserID, action, targetKind, targetID, []byte(metadata))
		return err
	})
}

func ListAuditEvents(ctx context.Context, s tenant.Scope, limit int) ([]AuditEvent, error) {
	var out []AuditEvent
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, actor_user_id, action, target_kind, target_id, metadata, created_at
			FROM audit_events ORDER BY created_at DESC LIMIT $1`, limit)
	})
	return out, err
}
