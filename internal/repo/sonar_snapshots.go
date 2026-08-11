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

type SonarSnapshot struct {
	ID          uuid.UUID `db:"id"`
	OrgID       uuid.UUID `db:"org_id"`
	BindingID   uuid.UUID `db:"binding_id"`
	ComponentID uuid.UUID `db:"component_id"`
	CapturedAt  time.Time `db:"captured_at"`
	ContentHash *string   `db:"content_hash"`
	CollectedAt time.Time `db:"collected_at"`

	QualityGate            *string    `db:"quality_gate"`
	Coverage               *float64   `db:"coverage"`
	Bugs                   *int       `db:"bugs"`
	Vulnerabilities        *int       `db:"vulnerabilities"`
	CodeSmells             *int       `db:"code_smells"`
	SecurityHotspots       *int       `db:"security_hotspots"`
	DuplicatedLinesDensity *float64   `db:"duplicated_lines_density"`
	NCLOC                  *int       `db:"ncloc"`
	Complexity             *int       `db:"complexity"`
	ReliabilityRating      *string    `db:"reliability_rating"`
	SecurityRating         *string    `db:"security_rating"`
	SqaleRating            *string    `db:"sqale_rating"`
	SqaleIndexMin          *int       `db:"sqale_index_min"`
	TestExecutionTimeMs    *int       `db:"test_execution_time_ms"`
	Languages              *string    `db:"languages"`
	LastScanAt             *time.Time `db:"last_scan_at"`

	RawJSON []byte `db:"raw_json"`
}

type NewSonarSnapshot struct {
	BindingID   uuid.UUID
	ComponentID uuid.UUID

	QualityGate            *string
	Coverage               *float64
	Bugs                   *int
	Vulnerabilities        *int
	CodeSmells             *int
	SecurityHotspots       *int
	DuplicatedLinesDensity *float64
	NCLOC                  *int
	Complexity             *int
	ReliabilityRating      *string
	SecurityRating         *string
	SqaleRating            *string
	SqaleIndexMin          *int
	TestExecutionTimeMs    *int
	Languages              *string
	LastScanAt             *time.Time

	RawJSON json.RawMessage
}

// InsertSonarSnapshot follows InsertGitlabSnapshot's own dedupe +
// dashboard-projection-upsert contract; see that function's doc comment.
func InsertSonarSnapshot(ctx context.Context, s tenant.Scope, in NewSonarSnapshot, contentHash string, headline collector.Headline) (id uuid.UUID, inserted bool, err error) {
	return snapshotWrite(ctx, s, "sonar_snapshots", in.ComponentID, in.BindingID, "sonarqube", contentHash, headline,
		func(tx *sqlx.Tx) (uuid.UUID, error) {
			var id uuid.UUID
			err := tx.GetContext(ctx, &id, `
				INSERT INTO sonar_snapshots (
					org_id, binding_id, component_id, content_hash,
					quality_gate, coverage, bugs, vulnerabilities, code_smells, security_hotspots,
					duplicated_lines_density, ncloc, complexity,
					reliability_rating, security_rating, sqale_rating, sqale_index_min, test_execution_time_ms,
					languages, last_scan_at, raw_json
				) VALUES (
					$1, $2, $3, $4,
					$5, $6, $7, $8, $9, $10,
					$11, $12, $13,
					$14, $15, $16, $17, $18,
					$19, $20, $21
				) RETURNING id`,
				s.OrgID(), in.BindingID, in.ComponentID, contentHash,
				in.QualityGate, in.Coverage, in.Bugs, in.Vulnerabilities, in.CodeSmells, in.SecurityHotspots,
				in.DuplicatedLinesDensity, in.NCLOC, in.Complexity,
				in.ReliabilityRating, in.SecurityRating, in.SqaleRating, in.SqaleIndexMin, in.TestExecutionTimeMs,
				in.Languages, in.LastScanAt, nullIfEmptyJSONBytes(in.RawJSON))
			return id, err
		})
}

func LatestSonarSnapshot(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) (*SonarSnapshot, error) {
	var out SonarSnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT * FROM sonar_snapshots WHERE binding_id = $1 ORDER BY collected_at DESC LIMIT 1`, bindingID)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// SonarSnapshotAt returns the snapshot captured at or before asOf — task
// 4.4's historical date picker, ported from the legacy Lotus's
// SonarPerServiceAsOf (same "MAX id captured_at <= cutoff" idea, here
// scoped to one binding instead of joined across every service). asOf
// nil means "latest", matching LatestSonarSnapshot above exactly (kept
// as a separate function rather than folding the nil case in here, so a
// caller that always wants the plain latest doesn't have to reason about
// the pointer).
func SonarSnapshotAt(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, asOf *time.Time) (*SonarSnapshot, error) {
	var out SonarSnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if asOf == nil {
			return tx.GetContext(ctx, &out, `
				SELECT * FROM sonar_snapshots WHERE binding_id = $1 ORDER BY captured_at DESC LIMIT 1`, bindingID)
		}
		return tx.GetContext(ctx, &out, `
			SELECT * FROM sonar_snapshots WHERE binding_id = $1 AND captured_at <= $2
			ORDER BY captured_at DESC LIMIT 1`, bindingID, *asOf)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// SonarSnapshotDatesForBinding returns every distinct calendar date (most
// recent first) this binding has at least one snapshot for — populates
// the date picker's option list.
func SonarSnapshotDatesForBinding(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]string, error) {
	var out []string
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT DISTINCT to_char(captured_at, 'YYYY-MM-DD') FROM sonar_snapshots
			WHERE binding_id = $1 ORDER BY 1 DESC`, bindingID)
	})
	return out, err
}
