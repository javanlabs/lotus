package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/tenant"
)

// ComponentMetric is one component's current dashboard row — task 3.6's
// projection. metrics is decoded into a plain map here rather than a
// fixed struct, on purpose: reading it generically (by kind name, not by
// a hardcoded field per kind) is what lets a fifth integration show up on
// the dashboard without touching this type.
type ComponentMetric struct {
	OrgID       uuid.UUID `db:"org_id"`
	ProjectID   uuid.UUID `db:"project_id"`
	ComponentID uuid.UUID `db:"component_id"`
	Status      string    `db:"status"`
	Score       *int      `db:"score"`
	MetricsJSON []byte    `db:"metrics"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// Metrics decodes MetricsJSON into kind -> KindHeadline.
func (c ComponentMetric) Metrics() map[string]KindHeadline {
	var out map[string]KindHeadline
	_ = json.Unmarshal(c.MetricsJSON, &out)
	return out
}

// KindHeadline is collector.Headline's own on-the-wire shape inside
// component_metrics_current.metrics — same fields, JSON tags added so a
// fifth integration's Headline round-trips through the jsonb column with
// no schema change (task 3.6's own point: "keyed by kind name... so a
// fifth integration doesn't add a migration").
type KindHeadline struct {
	Status     string `json:"status"`
	Score      *int   `json:"score,omitempty"`
	Grade      string `json:"grade,omitempty"`
	CountValue *int64 `json:"count_value,omitempty"`
	CountLabel string `json:"count_label,omitempty"`
	StateLabel string `json:"state_label,omitempty"`
	Summary    string `json:"summary,omitempty"`
	DeepLink   string `json:"deep_link,omitempty"`
	// UpdatedAt is when this kind was last collected successfully — set by
	// the database on every upsert, deduped snapshot or not, so it means
	// "last successful sync" rather than "last change". The row's own
	// updated_at is only the newest of these, which hides one integration
	// that stopped syncing behind the others that still do. Nil only on a
	// row written before migration 00034 backfilled it.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func kindHeadlineFrom(h collector.Headline) KindHeadline {
	return KindHeadline{
		Status: string(h.Status), Score: h.Score, Grade: h.Grade,
		CountValue: h.CountValue, CountLabel: h.CountLabel, StateLabel: h.StateLabel, Summary: h.Summary, DeepLink: h.DeepLink,
	}
}

// Status returns the rollup status as collector.Status, for callers that
// want the typed constant rather than a bare string.
func (c ComponentMetric) StatusValue() collector.Status { return collector.Status(c.Status) }

// UpsertComponentMetric merges kind's headline into componentID's
// projection row (creating it on first write) and recomputes the row's
// own rollup status/score from every kind now present (ComponentRollup:
// the average of the kinds that have a score, rounded once, and the
// status read from that score). Opens its
// own transaction; snapshotWrite (snapshot_write.go) calls the
// tx-scoped sibling of this instead when it needs the upsert to land
// atomically with a detail write.
func UpsertComponentMetric(ctx context.Context, s tenant.Scope, componentID uuid.UUID, kind string, headline collector.Headline) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		projectID, err := componentProjectID(ctx, tx, componentID)
		if err != nil {
			return err
		}
		return upsertComponentMetricTx(ctx, tx, s.OrgID(), projectID, componentID, kind, headline)
	})
}

func upsertComponentMetricTx(ctx context.Context, tx *sqlx.Tx, orgID, projectID, componentID uuid.UUID, kind string, headline collector.Headline) error {
	kindJSON, err := json.Marshal(kindHeadlineFrom(headline))
	if err != nil {
		return err
	}

	var mergedJSON []byte
	err = tx.QueryRowContext(ctx, `
		INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
		VALUES ($1, $2, $3, jsonb_build_object($4::text, $5::jsonb || jsonb_build_object('updated_at', now())), 'unknown', NULL, now())
		ON CONFLICT (component_id) DO UPDATE SET
			metrics = component_metrics_current.metrics || jsonb_build_object($4::text, $5::jsonb || jsonb_build_object('updated_at', now())),
			updated_at = now()
		RETURNING metrics`,
		orgID, projectID, componentID, kind, kindJSON).Scan(&mergedJSON)
	if err != nil {
		return err
	}

	return rollupProjectionTx(ctx, tx, componentID, mergedJSON)
}

// dropKindFromProjectionTx removes kind from componentID's projection row
// and recomputes the row's rollup from the kinds left. A row with no kind
// left keeps existing with status unknown, as before its first collect.
func dropKindFromProjectionTx(ctx context.Context, tx *sqlx.Tx, componentID uuid.UUID, kind string) error {
	var left []byte
	err := tx.QueryRowContext(ctx, `
		UPDATE component_metrics_current SET metrics = metrics - $2::text
		WHERE component_id = $1
		RETURNING metrics`, componentID, kind).Scan(&left)
	if isNoRows(err) {
		return nil // never collected: nothing to drop
	}
	if err != nil {
		return err
	}
	return rollupProjectionTx(ctx, tx, componentID, left)
}

// rollupProjectionTx sets componentID's rollup status and score from its
// kinds (metricsJSON), then rewrites its project's status for today in the
// same transaction, so the public status page's history never disagrees
// with the projection it was taken from.
func rollupProjectionTx(ctx context.Context, tx *sqlx.Tx, componentID uuid.UUID, metricsJSON []byte) error {
	var merged map[string]KindHeadline
	if err := json.Unmarshal(metricsJSON, &merged); err != nil {
		return err
	}
	rollupStatus, rollupScore := ComponentRollup(merged)

	var projectID uuid.UUID
	if err := tx.GetContext(ctx, &projectID, `
		UPDATE component_metrics_current SET status = $1, score = $2 WHERE component_id = $3
		RETURNING project_id`,
		string(rollupStatus), rollupScore, componentID); err != nil {
		return err
	}
	return recordProjectDayTx(ctx, tx, projectID)
}

// ListComponentMetricsForProject is the dashboard's own read path: every
// component in one project, in one query, no join — task 3.6's whole
// reason for existing (the legacy dashboard ran a MAX(id) GROUP BY
// subquery per provider per request; this is a single index range scan).
func ListComponentMetricsForProject(ctx context.Context, s tenant.Scope, projectID uuid.UUID) ([]ComponentMetric, error) {
	var out []ComponentMetric
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT org_id, project_id, component_id, status, score, metrics, updated_at
			FROM component_metrics_current WHERE project_id = $1`, projectID)
	})
	return out, err
}

// ListComponentMetricsForOrg is the same read, scoped to every component
// in the organization — the fleet-wide view.
func ListComponentMetricsForOrg(ctx context.Context, s tenant.Scope) ([]ComponentMetric, error) {
	var out []ComponentMetric
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT org_id, project_id, component_id, status, score, metrics, updated_at
			FROM component_metrics_current WHERE org_id = $1`, s.OrgID())
	})
	return out, err
}

// GetComponentMetric returns one component's projection row, or
// ErrNotFound if it has never had a successful Collect.
func GetComponentMetric(ctx context.Context, s tenant.Scope, componentID uuid.UUID) (*ComponentMetric, error) {
	var out ComponentMetric
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT org_id, project_id, component_id, status, score, metrics, updated_at
			FROM component_metrics_current WHERE component_id = $1`, componentID)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// ReprojectComponentMetric replaces kinds' headlines on componentID's
// projection row with ones rebuilt from stored snapshots (the reproject
// command), then recomputes the row's rollup and today's status day, as a
// collect would. A kind the row does not already carry is skipped — a
// reprojection never invents a metric that was never collected — and each
// kind keeps its updated_at, so it still reads as last synced when it
// last synced, not as fresh. Returns whether the row exists.
func ReprojectComponentMetric(ctx context.Context, s tenant.Scope, componentID uuid.UUID, kinds map[string]KindHeadline) (bool, error) {
	found := false
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		var raw []byte
		err := tx.GetContext(ctx, &raw, `SELECT metrics FROM component_metrics_current WHERE component_id = $1 FOR UPDATE`, componentID)
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var current map[string]KindHeadline
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		for kind, kh := range kinds {
			old, ok := current[kind]
			if !ok {
				continue
			}
			kh.UpdatedAt = old.UpdatedAt
			current[kind] = kh
		}
		merged, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE component_metrics_current SET metrics = $2 WHERE component_id = $1`, componentID, merged); err != nil {
			return err
		}
		return rollupProjectionTx(ctx, tx, componentID, merged)
	})
	return found, err
}

// KindHeadlineFrom is a collector headline in the projection's shape.
func KindHeadlineFrom(h collector.Headline) KindHeadline { return kindHeadlineFrom(h) }
