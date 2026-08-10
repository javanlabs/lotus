package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

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

// SentryIssue is one issue row within a snapshot.
type SentryIssue struct {
	ID         uuid.UUID `db:"id"`
	OrgID      uuid.UUID `db:"org_id"`
	SnapshotID uuid.UUID `db:"snapshot_id"`

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

// ReplaceSentrySnapshots atomically swaps every environment snapshot for
// one binding: delete everything currently stored for binding_id, then
// insert the fresh set — task 3.5's own simplification of the legacy
// REPLACE-per-environment-plus-a-separate-prune-pass into one
// binding-scoped operation. A Collect always returns every environment
// the binding is configured for in one Result (never an incremental
// subset), so a full delete+reinsert can never lose an environment the
// caller didn't mean to drop; an environment missing from the fresh set
// (because Sentry stopped reporting it) is exactly what should disappear.
func ReplaceSentrySnapshots(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, snaps []NewSentrySnapshot) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sentry_snapshots WHERE binding_id = $1`, bindingID); err != nil {
			return err
		}
		for _, snap := range snaps {
			var snapID uuid.UUID
			err := tx.GetContext(ctx, &snapID, `
				INSERT INTO sentry_snapshots (
					org_id, binding_id, component_id, environment, service_name,
					sentry_project_id, project_slug, project_name, platform, sentry_org,
					team_id, team_slug, status_filter, generated_at, total_issues,
					score, grade, load
				) VALUES (
					$1, $2, $3, $4, $5,
					$6, $7, $8, $9, $10,
					$11, $12, $13, $14, $15,
					$16, $17, $18
				) RETURNING id`,
				s.OrgID(), bindingID, snap.ComponentID, snap.Environment, snap.ServiceName,
				snap.SentryProjectID, snap.ProjectSlug, snap.ProjectName, snap.Platform, snap.SentryOrg,
				snap.TeamID, snap.TeamSlug, snap.StatusFilter, snap.GeneratedAt, snap.TotalIssues,
				snap.Score, snap.Grade, snap.Load)
			if err != nil {
				return err
			}
			for _, is := range snap.Issues {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO sentry_issues (
						org_id, snapshot_id, issue_id, short_id, title, status, substatus,
						event_count, first_seen, last_seen, permalink
					) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
					s.OrgID(), snapID, is.IssueID, is.ShortID, is.Title, is.Status, is.Substatus,
					is.EventCount, is.FirstSeen, is.LastSeen, is.Permalink); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListSentrySnapshotsForBinding returns every environment currently
// stored for one binding.
func ListSentrySnapshotsForBinding(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]SentrySnapshot, error) {
	var out []SentrySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT id, org_id, binding_id, component_id, captured_at, environment, service_name,
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
			SELECT id, org_id, binding_id, component_id, captured_at, environment, service_name,
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
			SELECT id, org_id, snapshot_id, issue_id, short_id, title, status, substatus,
			       event_count, first_seen, last_seen, permalink
			FROM sentry_issues WHERE snapshot_id = $1 ORDER BY event_count DESC`, snapshotID)
	})
	return out, err
}
