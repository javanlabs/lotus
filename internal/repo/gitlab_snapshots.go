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

// GitlabSnapshot is task 3.3's append-only GitLab collect record — one
// row per Collect, never updated. Column-for-column port of the legacy
// schema (migration 00015's own comment), plus the standard tenant-plane
// preamble.
type GitlabSnapshot struct {
	ID          uuid.UUID `db:"id"`
	OrgID       uuid.UUID `db:"org_id"`
	BindingID   uuid.UUID `db:"binding_id"`
	ComponentID uuid.UUID `db:"component_id"`
	CapturedAt  time.Time `db:"captured_at"`
	ContentHash *string   `db:"content_hash"`
	CollectedAt time.Time `db:"collected_at"`

	PrimaryLanguage *string `db:"primary_language"`
	LanguagesJSON   []byte  `db:"languages_json"`
	DefaultBranch   *string `db:"default_branch"`
	BranchesTotal   *int    `db:"branches_total"`
	BranchesActive  *int    `db:"branches_active"`
	BranchesStale   *int    `db:"branches_stale"`

	DefaultBranchProtected *bool      `db:"default_branch_protected"`
	Visibility             *string    `db:"visibility"`
	Archived               *bool      `db:"archived"`
	WebURL                 *string    `db:"web_url"`
	LastActivityAt         *time.Time `db:"last_activity_at"`

	Commits30d        *int    `db:"commits_30d"`
	Commits30dCapped  *bool   `db:"commits_30d_capped"`
	LastCommitterName *string `db:"last_committer_name"`

	LastPipelineStatus      *string    `db:"last_pipeline_status"`
	LastPipelineID          *int       `db:"last_pipeline_id"`
	LastPipelineDurationSec *int       `db:"last_pipeline_duration_sec"`
	LastPipelineWebURL      *string    `db:"last_pipeline_web_url"`
	LastPipelineFinishedAt  *time.Time `db:"last_pipeline_finished_at"`
	LastPipelineRef         *string    `db:"last_pipeline_ref"`
	PipelineSuccessRate     *float64   `db:"pipeline_success_rate"`
	PipelineSampleSize      *int       `db:"pipeline_sample_size"`

	WarningsJSON []byte `db:"warnings_json"`
	RawJSON      []byte `db:"raw_json"`
}

type NewGitlabSnapshot struct {
	BindingID   uuid.UUID
	ComponentID uuid.UUID

	PrimaryLanguage *string
	LanguagesJSON   json.RawMessage
	DefaultBranch   *string
	BranchesTotal   *int
	BranchesActive  *int
	BranchesStale   *int

	DefaultBranchProtected *bool
	Visibility             *string
	Archived               *bool
	WebURL                 *string
	LastActivityAt         *time.Time

	Commits30d        *int
	Commits30dCapped  *bool
	LastCommitterName *string

	LastPipelineStatus      *string
	LastPipelineID          *int
	LastPipelineDurationSec *int
	LastPipelineWebURL      *string
	LastPipelineFinishedAt  *time.Time
	LastPipelineRef         *string
	PipelineSuccessRate     *float64
	PipelineSampleSize      *int

	WarningsJSON json.RawMessage
	RawJSON      json.RawMessage
}

// InsertGitlabSnapshot writes in as a fresh row unless contentHash matches
// the binding's most recent snapshot, in which case that row's
// collected_at is bumped instead (task 3.6's write dedupe) — either way,
// componentID's dashboard projection is upserted from headline in the
// same transaction. inserted reports which branch ran, mainly for tests.
func InsertGitlabSnapshot(ctx context.Context, s tenant.Scope, in NewGitlabSnapshot, contentHash string, headline collector.Headline) (id uuid.UUID, inserted bool, err error) {
	return snapshotWrite(ctx, s, "gitlab_snapshots", in.ComponentID, in.BindingID, "gitlab", contentHash, headline,
		func(tx *sqlx.Tx) (uuid.UUID, error) {
			var id uuid.UUID
			err := tx.GetContext(ctx, &id, `
				INSERT INTO gitlab_snapshots (
					org_id, binding_id, component_id, content_hash,
					primary_language, languages_json, default_branch, branches_total, branches_active, branches_stale,
					default_branch_protected, visibility, archived, web_url, last_activity_at,
					commits_30d, commits_30d_capped, last_committer_name,
					last_pipeline_status, last_pipeline_id, last_pipeline_duration_sec, last_pipeline_web_url,
					last_pipeline_finished_at, last_pipeline_ref, pipeline_success_rate, pipeline_sample_size,
					warnings_json, raw_json
				) VALUES (
					$1, $2, $3, $4,
					$5, $6, $7, $8, $9, $10,
					$11, $12, $13, $14, $15,
					$16, $17, $18,
					$19, $20, $21, $22,
					$23, $24, $25, $26,
					$27, $28
				) RETURNING id`,
				s.OrgID(), in.BindingID, in.ComponentID, contentHash,
				in.PrimaryLanguage, nullIfEmptyJSONBytes(in.LanguagesJSON), in.DefaultBranch, in.BranchesTotal, in.BranchesActive, in.BranchesStale,
				in.DefaultBranchProtected, in.Visibility, in.Archived, in.WebURL, in.LastActivityAt,
				in.Commits30d, in.Commits30dCapped, in.LastCommitterName,
				in.LastPipelineStatus, in.LastPipelineID, in.LastPipelineDurationSec, in.LastPipelineWebURL,
				in.LastPipelineFinishedAt, in.LastPipelineRef, in.PipelineSuccessRate, in.PipelineSampleSize,
				nullIfEmptyJSONBytes(in.WarningsJSON), nullIfEmptyJSONBytes(in.RawJSON))
			return id, err
		})
}

// LatestGitlabSnapshot returns the most recent row for a binding, or
// ErrNotFound if none exist yet.
func LatestGitlabSnapshot(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) (*GitlabSnapshot, error) {
	var out GitlabSnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &out, `
			SELECT * FROM gitlab_snapshots WHERE binding_id = $1 ORDER BY collected_at DESC LIMIT 1`, bindingID)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func nullIfEmptyJSONBytes(b json.RawMessage) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
