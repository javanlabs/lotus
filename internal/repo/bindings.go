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

// ErrBindingTargetTaken is metric_bindings' unique key, (component_id,
// kind) among live bindings: a component has at most one binding of each
// kind (migration 00035). Task 2.3 first allowed several — two GitLab
// repos on one component — but every reader (the component's projection
// row, the project's KPI cards, the per-kind cells) holds one value per
// kind, so a second binding only ever overwrote the first. Two repos are
// two components.
var ErrBindingTargetTaken = errors.New("this component already has a binding of this kind")

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
		return nil, bindingWriteErr(txErr)
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
// scheduler happens to get around to each. Only bindings that were on are
// marked frozen, so one turned off on purpose stays off when the connection
// recovers (see unfreezeBindings).
func FreezeBindingsForConnection(ctx context.Context, s tenant.Scope, connectionID uuid.UUID) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE metric_bindings SET enabled = false, frozen_at = now()
			WHERE connection_id = $1 AND enabled AND deleted_at IS NULL`, connectionID)
		return err
	})
}

// unfreezeBindings resumes what a failed sign-in froze: called in the same
// transaction that stores a new credential or records a passing test, since
// access can also be fixed on the provider's side. If it is still refused,
// the next sync simply freezes them again.
func unfreezeBindings(ctx context.Context, tx *sqlx.Tx, connectionID uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE metric_bindings SET enabled = true, frozen_at = NULL
		WHERE connection_id = $1 AND frozen_at IS NOT NULL AND deleted_at IS NULL`, connectionID)
	return err
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
			WHERE mb.kind = $1 AND mb.deleted_at IS NULL AND p.deleted_at IS NULL
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
		res, err := tx.ExecContext(ctx, `UPDATE metric_bindings SET enabled = $2, frozen_at = NULL WHERE id = $1 AND deleted_at IS NULL`, id, enabled)
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
		return softDeleteBindingTx(ctx, tx, id)
	})
}

// softDeleteBindingTx retires a binding and takes its kind out of the
// component's projection row: the component no longer reports that kind,
// so its last value must not keep counting in the KPI cards, or sit in
// the table going stale.
func softDeleteBindingTx(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) error {
	var b struct {
		ComponentID uuid.UUID `db:"component_id"`
		Kind        string    `db:"kind"`
	}
	err := tx.GetContext(ctx, &b, `
		UPDATE metric_bindings SET enabled = false, deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING component_id, kind`, id)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return dropKindFromProjectionTx(ctx, tx, b.ComponentID, b.Kind)
}

// ActiveBindingForKind is componentID's live binding of kind, or
// ErrNotFound when it has none.
func ActiveBindingForKind(ctx context.Context, s tenant.Scope, componentID uuid.UUID, kind string) (*Binding, error) {
	var out Binding
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `SELECT `+bindingColumns+` FROM metric_bindings
			WHERE component_id = $1 AND kind = $2 AND deleted_at IS NULL`, componentID, kind)
	})
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplaceBinding retires oldID and creates in in its place, in one
// transaction, so the component is never left without the kind or with
// two of it. oldID must be the live binding of in.Kind on in.ComponentID.
// The old binding's snapshots and run history stay with it.
func ReplaceBinding(ctx context.Context, s tenant.Scope, oldID uuid.UUID, in NewBinding) (*Binding, error) {
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
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM metric_bindings
			WHERE id = $1 AND component_id = $2 AND kind = $3 AND deleted_at IS NULL`, oldID, in.ComponentID, in.Kind); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := softDeleteBindingTx(ctx, tx, oldID); err != nil {
			return err
		}
		return tx.GetContext(ctx, &out, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+bindingColumns,
			s.OrgID(), in.ComponentID, in.ConnectionID, in.Kind, in.ExternalKey, []byte(validated))
	})
	if txErr != nil {
		return nil, bindingWriteErr(txErr)
	}
	return &out, nil
}

// bindingWriteErr maps a metric_bindings insert's constraint errors.
func bindingWriteErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrBindingTargetTaken
		case "23503":
			return ErrNotFound // component_id/connection_id doesn't belong to this org
		case "23514":
			return ErrInvalidInput
		}
	}
	return err
}

// ProjectBinding is one live binding of a kind in a project, with what a
// project-level integration page needs to link out: the connection's base
// URL and config (Sentry keeps its organization slug there).
type ProjectBinding struct {
	BindingID        uuid.UUID       `db:"binding_id"`
	ComponentID      uuid.UUID       `db:"component_id"`
	ExternalKey      string          `db:"external_key"`
	Enabled          bool            `db:"enabled"`
	ConnectionName   string          `db:"connection_name"`
	ConnectionURL    string          `db:"connection_base_url"`
	ConnectionConfig json.RawMessage `db:"connection_config"`
	Config           json.RawMessage `db:"config"`
}

// RepoScan reports whether b is a repository-scan Trivy binding
// (IsRepoScan): its connection is GitLab and its key a project path.
func (b ProjectBinding) RepoScan() bool {
	return IsRepoScan(&Binding{Kind: "trivy", Config: b.Config})
}

// ListProjectBindingsByKind is every live binding of kind on the project's
// unarchived components — at most one per component (migration 00035) —
// in one query, joined to its connection.
func ListProjectBindingsByKind(ctx context.Context, s tenant.Scope, projectID uuid.UUID, kind string) ([]ProjectBinding, error) {
	var out []ProjectBinding
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT mb.id AS binding_id, mb.component_id, mb.external_key, mb.enabled, mb.config,
			       ic.name AS connection_name, ic.base_url AS connection_base_url, ic.config AS connection_config
			FROM metric_bindings mb
			JOIN components c ON c.id = mb.component_id
			JOIN integration_connections ic ON ic.id = mb.connection_id
			WHERE c.project_id = $1 AND c.archived_at IS NULL
			  AND mb.kind = $2 AND mb.deleted_at IS NULL
			ORDER BY c.name`, projectID, kind)
	})
	return out, err
}

// RepoScanSource marks a Trivy binding that scans a component's source
// repository instead of a container image: it sits on the component's
// GitLab connection, its external_key is the GitLab project path, and
// its config says {"source":"repo"}. internal/collector/trivy tells it
// apart from a Harbor binding by the connection's provider. Its
// snapshots carry the same word as their trivy_snapshots.source.
const RepoScanSource = "repo"

// RepoScanInterval is how often a repository scan runs: a clone and a
// full `trivy fs` are far heavier than an API read, and a dependency's
// new CVE is news on the scale of days.
const RepoScanInterval = 24 * time.Hour

// IsRepoScan reports whether b is a repository-scan Trivy binding.
func IsRepoScan(b *Binding) bool {
	if b == nil || b.Kind != "trivy" {
		return false
	}
	var cfg struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(b.Config, &cfg)
	return cfg.Source == RepoScanSource
}

// EnsureRepoScanBindings gives every component that has a GitLab binding
// but no container image to scan a repository-scan Trivy binding — the
// fallback for "no image set". A component that has any live Trivy
// binding keeps it (an image scan wins), and one whose repository scan
// was deleted is left alone: removing it is how a person turns it off.
// Idempotent; returns how many it created.
func EnsureRepoScanBindings(ctx context.Context, s tenant.Scope) (int, error) {
	var n int64
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config, sync_interval_seconds)
			SELECT g.org_id, g.component_id, g.connection_id, 'trivy', g.external_key,
			       jsonb_build_object('source', $1::text), $2
			FROM metric_bindings g
			JOIN components c ON c.id = g.component_id AND c.archived_at IS NULL
			WHERE g.kind = 'gitlab' AND g.deleted_at IS NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM metric_bindings t
			      WHERE t.component_id = g.component_id AND t.kind = 'trivy'
			        AND (t.deleted_at IS NULL OR t.config->>'source' = $1::text))
			ON CONFLICT DO NOTHING`, RepoScanSource, int(RepoScanInterval.Seconds()))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}
