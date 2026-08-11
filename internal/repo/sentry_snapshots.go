package repo

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/tenant"
)

// SentrySnapshot is one environment's worth of one binding's Sentry pull —
// task 3.5's redesign of the legacy sentry_snapshots table (see that
// migration's own comment): binding_id leads the unique key instead of
// project_slug, since two organizations can legitimately have identically
// named Sentry projects.
type SentrySnapshot struct {
	ID          uuid.UUID `db:"id"`
	OrgID       uuid.UUID `db:"org_id"`
	BindingID   uuid.UUID `db:"binding_id"`
	ComponentID uuid.UUID `db:"component_id"`
	CapturedAt  time.Time `db:"captured_at"`
	ContentHash *string   `db:"content_hash"`
	CollectedAt time.Time `db:"collected_at"`

	Environment     string `db:"environment"`
	ServiceName     string `db:"service_name"`
	SentryProjectID int64  `db:"sentry_project_id"`
	ProjectSlug     string `db:"project_slug"`
	ProjectName     string `db:"project_name"`
	Platform        string `db:"platform"`
	SentryOrg       string `db:"sentry_org"`
	TeamID          int64  `db:"team_id"`
	TeamSlug        string `db:"team_slug"`
	StatusFilter    string `db:"status_filter"`

	GeneratedAt *time.Time `db:"generated_at"`
	TotalIssues int        `db:"total_issues"`

	Score *int     `db:"score"`
	Grade *string  `db:"grade"`
	Load  *float64 `db:"load"`
}

// SentryIssue is one issue row within a snapshot. SnapshotCapturedAt is
// denormalized from its parent row so internal/retention can delete a
// partition's issues by the same captured_at range it's dropping,
// without a join back to (a possibly already-gone) sentry_snapshots —
// there is deliberately no FK here at all; see migration 00018's own
// comment on why one is impossible for a table referencing a partitioned
// parent that must still be droppable partition-by-partition.
type SentryIssue struct {
	ID                 uuid.UUID `db:"id"`
	OrgID              uuid.UUID `db:"org_id"`
	SnapshotID         uuid.UUID `db:"snapshot_id"`
	SnapshotCapturedAt time.Time `db:"snapshot_captured_at"`

	IssueID    string     `db:"issue_id"`
	ShortID    string     `db:"short_id"`
	Title      string     `db:"title"`
	Status     string     `db:"status"`
	Substatus  string     `db:"substatus"`
	EventCount int        `db:"event_count"`
	FirstSeen  *time.Time `db:"first_seen"`
	LastSeen   *time.Time `db:"last_seen"`
	Permalink  string     `db:"permalink"`
}

type NewSentryIssue struct {
	IssueID    string
	ShortID    string
	Title      string
	Status     string
	Substatus  string
	EventCount int
	FirstSeen  *time.Time
	LastSeen   *time.Time
	Permalink  string
}

type NewSentrySnapshot struct {
	ComponentID uuid.UUID
	ContentHash string

	Environment     string
	ServiceName     string
	SentryProjectID int64
	ProjectSlug     string
	ProjectName     string
	Platform        string
	SentryOrg       string
	TeamID          int64
	TeamSlug        string
	StatusFilter    string

	GeneratedAt *time.Time
	TotalIssues int

	Score *int
	Grade *string
	Load  *float64

	Issues []NewSentryIssue
}

// ReplaceSentrySnapshots swaps every environment snapshot for one
// binding: any environment no longer in snaps is dropped (task 3.5's
// per-binding environment pruning), and each environment that is present
// gets either a bumped captured_at (its content_hash matches what's
// already stored — task 3.6's write dedupe) or a fresh row (cascading a
// delete of its old one first, if it had one, so its issues never
// accumulate across collects). Finishes by upserting componentID's
// dashboard projection row from headline, atomically with everything
// above — task 3.6's "same transaction as the snapshot write" rule.
func ReplaceSentrySnapshots(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, headline collector.Headline, snaps []NewSentrySnapshot) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		keepEnvs := make([]string, 0, len(snaps))
		for _, snap := range snaps {
			keepEnvs = append(keepEnvs, snap.Environment)
		}
		// sentry_snapshots has no FK from sentry_issues (see migration
		// 00018's own comment on why not — partitioning), so every delete
		// below removes the matching sentry_issues rows explicitly first;
		// nothing here cascades.
		if len(keepEnvs) == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sentry_issues WHERE snapshot_id IN (SELECT id FROM sentry_snapshots WHERE binding_id = $1)`, bindingID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM sentry_snapshots WHERE binding_id = $1`, bindingID); err != nil {
				return err
			}
		} else {
			iq, iargs, err := sqlx.In(`DELETE FROM sentry_issues WHERE snapshot_id IN (
				SELECT id FROM sentry_snapshots WHERE binding_id = ? AND environment NOT IN (?))`, bindingID, keepEnvs)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, tx.Rebind(iq), iargs...); err != nil {
				return err
			}
			q, args, err := sqlx.In(`DELETE FROM sentry_snapshots WHERE binding_id = ? AND environment NOT IN (?)`, bindingID, keepEnvs)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, tx.Rebind(q), args...); err != nil {
				return err
			}
		}

		for _, snap := range snaps {
			var existingID uuid.UUID
			var existingHash sql.NullString
			lookupErr := tx.QueryRowContext(ctx, `
				SELECT id, content_hash FROM sentry_snapshots WHERE binding_id = $1 AND environment = $2 AND service_name = $3`,
				bindingID, snap.Environment, snap.ServiceName).Scan(&existingID, &existingHash)
			switch {
			case lookupErr != nil && !isNoRows(lookupErr):
				return lookupErr
			case lookupErr == nil && snap.ContentHash != "" && existingHash.Valid && existingHash.String == snap.ContentHash:
				// Bump collected_at only — never captured_at, which is
				// the partition key; see snapshot_write.go's
				// dedupeOrTouch for the same rule applied to the other
				// three kinds.
				if _, err := tx.ExecContext(ctx, `UPDATE sentry_snapshots SET collected_at = now() WHERE id = $1`, existingID); err != nil {
					return err
				}
				continue
			case lookupErr == nil:
				// A row exists for this (binding, environment, service)
				// but its content changed — replace it. No FK/CASCADE
				// here (see migration 00018's comment), so its old issues
				// are deleted explicitly first.
				if _, err := tx.ExecContext(ctx, `DELETE FROM sentry_issues WHERE snapshot_id = $1`, existingID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM sentry_snapshots WHERE id = $1`, existingID); err != nil {
					return err
				}
			}

			var snapID uuid.UUID
			var capturedAt time.Time
			err := tx.QueryRowContext(ctx, `
				INSERT INTO sentry_snapshots (
					org_id, binding_id, component_id, content_hash, environment, service_name,
					sentry_project_id, project_slug, project_name, platform, sentry_org,
					team_id, team_slug, status_filter, generated_at, total_issues,
					score, grade, load
				) VALUES (
					$1, $2, $3, $4, $5, $6,
					$7, $8, $9, $10, $11,
					$12, $13, $14, $15, $16,
					$17, $18, $19
				) RETURNING id, captured_at`,
				s.OrgID(), bindingID, snap.ComponentID, snap.ContentHash, snap.Environment, snap.ServiceName,
				snap.SentryProjectID, snap.ProjectSlug, snap.ProjectName, snap.Platform, snap.SentryOrg,
				snap.TeamID, snap.TeamSlug, snap.StatusFilter, snap.GeneratedAt, snap.TotalIssues,
				snap.Score, snap.Grade, snap.Load).Scan(&snapID, &capturedAt)
			if err != nil {
				return err
			}
			for _, is := range snap.Issues {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO sentry_issues (
						org_id, snapshot_id, snapshot_captured_at, issue_id, short_id, title, status, substatus,
						event_count, first_seen, last_seen, permalink
					) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
					s.OrgID(), snapID, capturedAt, is.IssueID, is.ShortID, is.Title, is.Status, is.Substatus,
					is.EventCount, is.FirstSeen, is.LastSeen, is.Permalink); err != nil {
					return err
				}
			}
		}

		projectID, err := componentProjectID(ctx, tx, componentID)
		if err != nil {
			return err
		}
		return upsertComponentMetricTx(ctx, tx, s.OrgID(), projectID, componentID, "sentry", headline)
	})
}

// ListSentrySnapshotsForBinding returns every environment currently
// stored for one binding.
func ListSentrySnapshotsForBinding(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]SentrySnapshot, error) {
	var out []SentrySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, binding_id, component_id, captured_at, content_hash, collected_at, environment, service_name,
			       sentry_project_id, project_slug, project_name, platform, sentry_org,
			       team_id, team_slug, status_filter, generated_at, total_issues, score, grade, load
			FROM sentry_snapshots WHERE binding_id = $1 ORDER BY environment`, bindingID)
	})
	return out, err
}

// ListSentrySnapshotsForComponent returns every environment of every
// Sentry binding on a component — a component with two Sentry bindings
// (e.g. a fanned-out backend split into two Sentry projects mid-migration)
// shows every one of them, not just one.
func ListSentrySnapshotsForComponent(ctx context.Context, s tenant.Scope, componentID uuid.UUID) ([]SentrySnapshot, error) {
	var out []SentrySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, binding_id, component_id, captured_at, content_hash, collected_at, environment, service_name,
			       sentry_project_id, project_slug, project_name, platform, sentry_org,
			       team_id, team_slug, status_filter, generated_at, total_issues, score, grade, load
			FROM sentry_snapshots WHERE component_id = $1 ORDER BY binding_id, environment`, componentID)
	})
	return out, err
}

// ListSentryIssuesForSnapshot returns one snapshot's stored issues,
// highest event count first.
func ListSentryIssuesForSnapshot(ctx context.Context, s tenant.Scope, snapshotID uuid.UUID) ([]SentryIssue, error) {
	var out []SentryIssue
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, snapshot_id, snapshot_captured_at, issue_id, short_id, title, status, substatus,
			       event_count, first_seen, last_seen, permalink
			FROM sentry_issues WHERE snapshot_id = $1 ORDER BY event_count DESC`, snapshotID)
	})
	return out, err
}
