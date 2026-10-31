package repo

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/tenant"
)

type DiscoveredResource struct {
	ID             uuid.UUID       `db:"id"`
	OrgID          uuid.UUID       `db:"org_id"`
	ConnectionID   uuid.UUID       `db:"connection_id"`
	Kind           string          `db:"kind"`
	ExternalKey    string          `db:"external_key"`
	Label          string          `db:"label"`
	Metadata       json.RawMessage `db:"metadata"`
	BoundBindingID *uuid.UUID      `db:"bound_binding_id"`
	IgnoredAt      *time.Time      `db:"ignored_at"`
	FirstSeenAt    time.Time       `db:"first_seen_at"`
	LastSeenAt     time.Time       `db:"last_seen_at"`
}

const discoveredResourceColumns = `id, org_id, connection_id, kind, external_key, label, metadata,
	bound_binding_id, ignored_at, first_seen_at, last_seen_at`

// UpsertDiscoveredResources records what Discover found. A resource that
// existed before and is still present gets label/metadata refreshed and
// last_seen_at bumped; a resource that's gone missing from items simply
// isn't touched at all — task 2.4: "resource yang hilang dari upstream
// nggak dihapus, cuma last_seen_at-nya beku" — freezing is the natural
// result of never running its UPDATE, not a separate mechanism.
func UpsertDiscoveredResources(ctx context.Context, s tenant.Scope, connectionID uuid.UUID, kind string, items []integrations.DiscoveredItem) (int, error) {
	n := 0
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		for _, item := range items {
			meta, merr := json.Marshal(item.Metadata)
			if merr != nil {
				return merr
			}
			if len(meta) == 0 {
				meta = json.RawMessage(`{}`)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO discovered_resources (org_id, connection_id, kind, external_key, label, metadata, last_seen_at)
				VALUES ($1, $2, $3, $4, $5, $6, now())
				ON CONFLICT (connection_id, kind, external_key)
				DO UPDATE SET label = EXCLUDED.label, metadata = EXCLUDED.metadata, last_seen_at = now()`,
				s.OrgID(), connectionID, kind, item.ExternalKey, item.Label, []byte(meta)); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

type DiscoveredResourceFilter struct {
	ConnectionID  *uuid.UUID
	UnclaimedOnly bool // excludes both bound and ignored resources — task 2.4's "unclaimed panel" default
	Search        string
}

func ListDiscoveredResources(ctx context.Context, s tenant.Scope, filter DiscoveredResourceFilter) ([]DiscoveredResource, error) {
	var out []DiscoveredResource
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		q := `SELECT ` + discoveredResourceColumns + ` FROM discovered_resources WHERE true`
		var args []any
		if filter.ConnectionID != nil {
			args = append(args, *filter.ConnectionID)
			q += ` AND connection_id = $` + strconv.Itoa(len(args))
		}
		if filter.UnclaimedOnly {
			q += ` AND bound_binding_id IS NULL AND ignored_at IS NULL`
		}
		if filter.Search != "" {
			args = append(args, "%"+filter.Search+"%")
			n := strconv.Itoa(len(args))
			q += ` AND (external_key ILIKE $` + n + ` OR label ILIKE $` + n + `)`
		}
		q += ` ORDER BY label, external_key`
		return tx.SelectContext(ctx, &out, q, args...)
	})
	return out, err
}

func SetDiscoveredResourceIgnored(ctx context.Context, s tenant.Scope, id uuid.UUID, ignored bool) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		expr := "now()"
		if !ignored {
			expr = "NULL"
		}
		res, err := tx.ExecContext(ctx, `UPDATE discovered_resources SET ignored_at = `+expr+` WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}

// ClaimTarget describes what one selected discovered resource should
// become: either an existing Project/Component, or one to create fresh.
// Exactly one of ExistingProjectID / (NewProjectSlug, NewProjectName) must
// be set, and likewise for the component pair — the handler validates
// this before calling ClaimDiscoveredResources.
type ClaimTarget struct {
	DiscoveredResourceID uuid.UUID
	ExistingProjectID    *uuid.UUID
	NewProjectSlug       string
	NewProjectName       string
	ExistingComponentID  *uuid.UUID
	NewComponentSlug     string
	NewComponentName     string
	NewComponentKind     string
	BindingConfig        json.RawMessage
}

type ClaimResult struct {
	DiscoveredResourceID uuid.UUID `json:"discovered_resource_id"`
	ProjectID            uuid.UUID `json:"project_id"`
	ComponentID          uuid.UUID `json:"component_id"`
	BindingID            uuid.UUID `json:"binding_id"`
}

// ClaimDiscoveredResources is task 2.4's "nyentang N resource bikin
// Project + Component + Binding sekaligus dalam satu aksi" — every claim
// in the batch runs inside one transaction, so a partial failure rolls
// back the whole batch rather than leaving some resources claimed and
// others not. Two claims that both create the same new project/component
// slug within the batch share one row (looked up via ON CONFLICT ...
// RETURNING, not a second INSERT) rather than colliding.
func ClaimDiscoveredResources(ctx context.Context, s tenant.Scope, connectionID uuid.UUID, bindingKind string, claims []ClaimTarget) ([]ClaimResult, error) {
	descriptor, err := integrations.DescriptorFor(bindingKind)
	if err != nil {
		return nil, ErrInvalidInput
	}

	var results []ClaimResult
	txErr := s.Tx(ctx, func(tx *sqlx.Tx) error {
		projectCache := map[string]uuid.UUID{}
		componentCache := map[string]uuid.UUID{}

		for _, claim := range claims {
			projectID, err := resolveProjectID(ctx, tx, s.OrgID(), claim, projectCache)
			if err != nil {
				return err
			}
			componentID, err := resolveComponentID(ctx, tx, s.OrgID(), projectID, claim, componentCache)
			if err != nil {
				return err
			}

			var externalKey string
			if err := tx.GetContext(ctx, &externalKey, `SELECT external_key FROM discovered_resources WHERE id = $1`, claim.DiscoveredResourceID); err != nil {
				return err
			}
			validatedConfig, err := descriptor.ValidateConfig(claim.BindingConfig)
			if err != nil {
				return err
			}

			var bindingID uuid.UUID
			if err := tx.GetContext(ctx, &bindingID, `
				INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
				s.OrgID(), componentID, connectionID, bindingKind, externalKey, []byte(validatedConfig)); err != nil {
				return bindingWriteErr(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE discovered_resources SET bound_binding_id = $2 WHERE id = $1`, claim.DiscoveredResourceID, bindingID); err != nil {
				return err
			}

			results = append(results, ClaimResult{
				DiscoveredResourceID: claim.DiscoveredResourceID, ProjectID: projectID, ComponentID: componentID, BindingID: bindingID,
			})
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return results, nil
}

func resolveProjectID(ctx context.Context, tx *sqlx.Tx, orgID uuid.UUID, claim ClaimTarget, cache map[string]uuid.UUID) (uuid.UUID, error) {
	if claim.ExistingProjectID != nil {
		return *claim.ExistingProjectID, nil
	}
	if cached, ok := cache[claim.NewProjectSlug]; ok {
		return cached, nil
	}
	var id uuid.UUID
	err := tx.GetContext(ctx, &id, `
		INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $3)
		ON CONFLICT (org_id, slug) WHERE deleted_at IS NULL DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, orgID, claim.NewProjectSlug, claim.NewProjectName)
	if err != nil {
		return uuid.Nil, err
	}
	cache[claim.NewProjectSlug] = id
	return id, nil
}

func resolveComponentID(ctx context.Context, tx *sqlx.Tx, orgID, projectID uuid.UUID, claim ClaimTarget, cache map[string]uuid.UUID) (uuid.UUID, error) {
	if claim.ExistingComponentID != nil {
		return *claim.ExistingComponentID, nil
	}
	key := projectID.String() + "/" + claim.NewComponentSlug
	if cached, ok := cache[key]; ok {
		return cached, nil
	}
	var id uuid.UUID
	err := tx.GetContext(ctx, &id, `
		INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, orgID, projectID, claim.NewComponentSlug, claim.NewComponentName, claim.NewComponentKind)
	if err != nil {
		return uuid.Nil, err
	}
	cache[key] = id
	return id, nil
}
