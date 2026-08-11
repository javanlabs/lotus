package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/tenant"
)

// ErrBindingTargetTaken mirrors ErrSlugTaken/ErrNameTaken for
// metric_bindings' unique key, (component_id, kind, external_key) —
// task 2.3 explicitly allows two GitLab bindings on one component (a
// frontend repo and a backend repo), so the key is the full triple, not
// just (component_id, kind).
var ErrBindingTargetTaken = errors.New("a binding with this kind and external_key already exists on this component")

type Binding struct {
	ID                  uuid.UUID       `db:"id"`
	OrgID               uuid.UUID       `db:"org_id"`
	ComponentID         uuid.UUID       `db:"component_id"`
	ConnectionID        uuid.UUID       `db:"connection_id"`
	Kind                string          `db:"kind"`
	ExternalKey         string          `db:"external_key"`
	Config              json.RawMessage `db:"config"`
	Enabled             bool            `db:"enabled"`
	SyncIntervalSeconds *int            `db:"sync_interval_seconds"`
	NextSyncAt          *time.Time      `db:"next_sync_at"`
	LastStatus          *string         `db:"last_status"`
	ConsecutiveFailures int             `db:"consecutive_failures"`
	DeletedAt           *time.Time      `db:"deleted_at"`
	CreatedAt           time.Time       `db:"created_at"`
}

const bindingColumns = `id, org_id, component_id, connection_id, kind, external_key, config, enabled,
	sync_interval_seconds, next_sync_at, last_status, consecutive_failures, deleted_at, created_at`

type NewBinding struct {
	ComponentID  uuid.UUID
	ConnectionID uuid.UUID
	Kind         string
	ExternalKey  string
	Config       json.RawMessage
}

// CreateBinding validates Config against Kind's Descriptor (task 2.3) —
// with defaults applied — before it's ever written; migration 00012's
// CHECK constraints are the safety net behind this, not a substitute for
// it (they can't express "conditionally required" nearly as clearly as
// Go can, and can't give a form-filler a field-specific error message).
func CreateBinding(ctx context.Context, s tenant.Scope, in NewBinding) (*Binding, error) {
	descriptor, err := integrations.DescriptorFor(in.Kind)
	if err != nil {
		return nil, ErrInvalidInput
	}
	validated, err := descriptor.ValidateConfig(in.Config)
	if err != nil {
		return nil, err
	}

	var out Binding
	txErr := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+bindingColumns,
			s.OrgID(), in.ComponentID, in.ConnectionID, in.Kind, in.ExternalKey, []byte(validated))
	})
	if txErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(txErr, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return nil, ErrBindingTargetTaken
			case "23503":
				return nil, ErrNotFound // component_id/connection_id doesn't belong to this org
			case "23514":
				return nil, ErrInvalidInput
			}
		}
		return nil, txErr
	}
	return &out, nil
}

func BindingByID(ctx context.Context, s tenant.Scope, id uuid.UUID) (*Binding, error) {
	var out Binding
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+bindingColumns+` FROM metric_bindings WHERE id = $1`, id)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// ListDueBindings returns every enabled, non-deleted binding in this
// org's Scope whose next_sync_at has arrived (or was never set — a brand
// new binding, task 3.2's "jalanin sekali langsung pas binding dibuat").
// The scheduler (task 3.2) calls this once per organization via
// tenant.FromRun, iterating every org in turn — see that package's own
// doc comment for why a single cross-tenant query isn't an option here.
func ListDueBindings(ctx context.Context, s tenant.Scope, now time.Time) ([]Binding, error) {
	var out []Binding
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+bindingColumns+` FROM metric_bindings
			WHERE enabled AND deleted_at IS NULL AND (next_sync_at IS NULL OR next_sync_at <= $1)`, now)
	})
	return out, err
}

// UpdateBindingAfterRun records a completed run's effect on its binding's
// own schedule and health bookkeeping: when it's due again, its last
// status, and its consecutive-failure count (task 3.1's not_found rule:
// 3 in a row marks it broken — see internal/scheduler's result handler
// for where that threshold is applied).
func UpdateBindingAfterRun(ctx context.Context, s tenant.Scope, id uuid.UUID, nextSyncAt time.Time, lastStatus string, consecutiveFailures int) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE metric_bindings
			SET next_sync_at = $2, last_status = $3, consecutive_failures = $4
			WHERE id = $1`, id, nextSyncAt, lastStatus, consecutiveFailures)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

// FreezeBindingsForConnection disables every binding on connectionID —
// task 3.1's auth-failure side effect: an expired credential must stop
// every binding under it at once, not fail them one at a time as the
// scheduler happens to get around to each.
func FreezeBindingsForConnection(ctx context.Context, s tenant.Scope, connectionID uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE metric_bindings SET enabled = false WHERE connection_id = $1 AND deleted_at IS NULL`, connectionID)
		return err
	})
}

// CountActiveBindingsForConnection is the pre-delete confirmation count
// task 4.5's /settings/connections spec calls for explicitly ("delete
// yang nampilin berapa binding bakal ikut mati") — read-only, so the UI
// can show the number before the user commits to the delete.
func CountActiveBindingsForConnection(ctx context.Context, s tenant.Scope, connectionID uuid.UUID) (int, error) {
	var n int
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, `
			SELECT count(*) FROM metric_bindings WHERE connection_id = $1 AND deleted_at IS NULL`, connectionID)
	})
	return n, err
}

// ListBindingsForComponent returns live bindings by default (deleted_at
// IS NULL) — soft-deleted bindings still exist for their history but
// don't belong in a normal listing.
func ListBindingsForComponent(ctx context.Context, s tenant.Scope, componentID uuid.UUID, includeDeleted bool) ([]Binding, error) {
	var out []Binding
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		q := `SELECT ` + bindingColumns + ` FROM metric_bindings WHERE component_id = $1`
		if !includeDeleted {
			q += ` AND deleted_at IS NULL`
		}
		q += ` ORDER BY created_at`
		return tx.SelectContext(ctx, &out, q, componentID)
	})
	return out, err
}

// BindingWithPath is one binding plus enough of its project/component
// chain to link back to it — /settings/ingest (task 4.5) needs a
// binding's ingest URL alongside where it actually lives, not just its id.
type BindingWithPath struct {
	Binding
	ProjectID     uuid.UUID `db:"project_id_path"`
	ProjectName   string    `db:"project_name"`
	ComponentName string    `db:"component_name"`
}

// ListBindingsForOrgByKind lists every live binding of kind across the
// whole organization, joined out to its project/component names — used
// by the ingest-docs screen, which is about "every push-ingestible
// binding in this org", not one component's own bindings the way
// ListBindingsForComponent above is.
func ListBindingsForOrgByKind(ctx context.Context, s tenant.Scope, kind string) ([]BindingWithPath, error) {
	var out []BindingWithPath
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT mb.id, mb.org_id, mb.component_id, mb.connection_id, mb.kind, mb.external_key, mb.config, mb.enabled,
			       mb.sync_interval_seconds, mb.next_sync_at, mb.last_status, mb.consecutive_failures, mb.deleted_at, mb.created_at,
			       p.id AS project_id_path, p.name AS project_name, c.name AS component_name
			FROM metric_bindings mb
			JOIN components c ON c.id = mb.component_id
			JOIN projects p ON p.id = c.project_id
			WHERE mb.kind = $1 AND mb.deleted_at IS NULL
			ORDER BY p.name, c.name`, kind)
	})
	return out, err
}

// UpdateBindingConfig re-validates config against the binding's own kind
// (kind itself is immutable — changing what a binding even is isn't a
// config edit, it's a different binding) before writing it.
func UpdateBindingConfig(ctx context.Context, s tenant.Scope, id uuid.UUID, config json.RawMessage) (*Binding, error) {
	existing, err := BindingByID(ctx, s, id)
	if err != nil {
		return nil, err
	}
	descriptor, err := integrations.DescriptorFor(existing.Kind)
	if err != nil {
		return nil, ErrInvalidInput
	}
	validated, err := descriptor.ValidateConfig(config)
	if err != nil {
		return nil, err
	}

	var out Binding
	txErr := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			UPDATE metric_bindings SET config = $2 WHERE id = $1
			RETURNING `+bindingColumns, id, []byte(validated))
	})
	if txErr != nil {
		if isNoRows(txErr) {
			return nil, ErrNotFound
		}
		return nil, txErr
	}
	return &out, nil
}

// SetBindingEnabled toggles scheduling without touching history — task
// 2.3: "Nonaktifin binding nyetop penjadwalannya tanpa ngapus history."
func SetBindingEnabled(ctx context.Context, s tenant.Scope, id uuid.UUID, enabled bool) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE metric_bindings SET enabled = $2 WHERE id = $1 AND deleted_at IS NULL`, id, enabled)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

// SoftDeleteBinding is the normal removal path — the binding stops being
// scheduled and disappears from listings, but its row (and therefore its
// history once Fase 3 attaches metric_facts to it) survives.
func SoftDeleteBinding(ctx context.Context, s tenant.Scope, id uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE metric_bindings SET enabled = false, deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}
