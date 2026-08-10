package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/uyab/lotus/internal/tenant"
)

type TrivySnapshot struct {
	ID          uuid.UUID `db:"id"`
	OrgID       uuid.UUID `db:"org_id"`
	BindingID   uuid.UUID `db:"binding_id"`
	ComponentID uuid.UUID `db:"component_id"`
	CapturedAt  time.Time `db:"captured_at"`

	Source string  `db:"source"`
	Digest *string `db:"digest"`
	// pq.StringArray, not []string: that's what makes sqlx's automatic
	// struct scanning understand Postgres's text[] wire format on the way
	// in and out — see internal/repo/global/api_tokens.go's own comment,
	// same reasoning.
	Tags           pq.StringArray `db:"tags"`
	Severity       *string        `db:"severity"`
	Total          int            `db:"total"`
	Fixable        int            `db:"fixable"`
	Critical       int            `db:"critical"`
	High           int            `db:"high"`
	Medium         int            `db:"medium"`
	Low            int            `db:"low"`
	Unknown        int            `db:"unknown"`
	Pulls          *int           `db:"pulls"`
	ArtifactCount  *int           `db:"artifact_count"`
	ImagePushedAt  *time.Time     `db:"image_pushed_at"`
	ScanFinishedAt *time.Time     `db:"scan_finished_at"`

	RawJSON []byte `db:"raw_json"`
}

type NewTrivySnapshot struct {
	BindingID   uuid.UUID
	ComponentID uuid.UUID

	Source         string
	Digest         *string
	Tags           []string
	Severity       *string
	Total          int
	Fixable        int
	Critical       int
	High           int
	Medium         int
	Low            int
	Unknown        int
	Pulls          *int
	ArtifactCount  *int
	ImagePushedAt  *time.Time
	ScanFinishedAt *time.Time

	RawJSON json.RawMessage
}

func InsertTrivySnapshot(ctx context.Context, s tenant.Scope, in NewTrivySnapshot) (uuid.UUID, error) {
	if in.Tags == nil {
		in.Tags = []string{}
	}
	var id uuid.UUID
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &id, `
			INSERT INTO trivy_snapshots (
				org_id, binding_id, component_id, source, digest, tags, severity,
				total, fixable, critical, high, medium, low, unknown,
				pulls, artifact_count, image_pushed_at, scan_finished_at, raw_json
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13, $14,
				$15, $16, $17, $18, $19
			) RETURNING id`,
			s.OrgID(), in.BindingID, in.ComponentID, in.Source, in.Digest, pq.Array(in.Tags), in.Severity,
			in.Total, in.Fixable, in.Critical, in.High, in.Medium, in.Low, in.Unknown,
			in.Pulls, in.ArtifactCount, in.ImagePushedAt, in.ScanFinishedAt, nullIfEmptyJSONBytes(in.RawJSON))
	})
	return id, err
}

func LatestTrivySnapshot(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) (*TrivySnapshot, error) {
	var out TrivySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT id, org_id, binding_id, component_id, captured_at, source, digest,
			       tags, severity, total, fixable, critical, high, medium, low, unknown,
			       pulls, artifact_count, image_pushed_at, scan_finished_at, raw_json
			FROM trivy_snapshots WHERE binding_id = $1 ORDER BY captured_at DESC LIMIT 1`, bindingID)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// ListTrivySnapshotsForComponent returns every binding's latest Trivy
// snapshot for a component — task 3.4's own acceptance criterion: a
// component with two Trivy bindings must show two cells, not one
// overwriting the other, since each is keyed by its own binding_id.
func ListTrivySnapshotsForComponent(ctx context.Context, s tenant.Scope, componentID uuid.UUID) ([]TrivySnapshot, error) {
	var out []TrivySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT DISTINCT ON (binding_id)
			       id, org_id, binding_id, component_id, captured_at, source, digest,
			       tags, severity, total, fixable, critical, high, medium, low, unknown,
			       pulls, artifact_count, image_pushed_at, scan_finished_at, raw_json
			FROM trivy_snapshots WHERE component_id = $1
			ORDER BY binding_id, captured_at DESC`, componentID)
	})
	return out, err
}
