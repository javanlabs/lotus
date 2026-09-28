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

// ReplaceSentrySnapshots records one sync of a Sentry binding: one
// snapshot per environment (and service tag). Like the other kinds, it
// keeps history: when an environment's content is unchanged its latest
// snapshot is only marked as seen again (collected_at), and when it
// changed a new snapshot is added beside the old one, whose issues stay.
// Every row a sync writes or touches carries the transaction's now() as
// collected_at, which is how SentrySnapshotsAt tells the latest sync's
// environments from ones that have since gone.
func ReplaceSentrySnapshots(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, headline collector.Headline, snaps []NewSentrySnapshot) error {
	// A historical write (WithObservedAt) happens at `at`: its predecessor
	// is the latest snapshot captured by then — never a later live one —
	// and it leaves the dashboard's current state alone.
	at, historical := observedAt(ctx)
	var atArg *time.Time
	if historical {
		atArg = &at
	}
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		for _, snap := range snaps {
			var latestID uuid.UUID
			var latestHash sql.NullString
			err := tx.QueryRowContext(ctx, `
				SELECT id, content_hash FROM sentry_snapshots
				WHERE binding_id = $1 AND environment = $2 AND service_name = $3
				  AND ($4::timestamptz IS NULL OR captured_at <= $4)
				ORDER BY captured_at DESC LIMIT 1`,
				bindingID, snap.Environment, snap.ServiceName, atArg).Scan(&latestID, &latestHash)
			if err != nil && !isNoRows(err) {
				return err
			}
			if err == nil && snap.ContentHash != "" && latestHash.Valid && latestHash.String == snap.ContentHash {
				// Unchanged: bump collected_at only — never captured_at,
				// the partition key (snapshot_write.go's dedupeOrTouch
				// applies the same rule to the other three kinds).
				// The score, grade and load decay with time and are not in
				// the hash: refresh them on the latest snapshot.
				if _, err := tx.ExecContext(ctx, `
					UPDATE sentry_snapshots SET
						collected_at = CASE WHEN $2::timestamptz IS NULL THEN now() ELSE GREATEST(collected_at, $2) END,
						generated_at = $3, score = $4, grade = $5, load = $6
					WHERE id = $1`, latestID, atArg, snap.GeneratedAt, snap.Score, snap.Grade, snap.Load); err != nil {
					return err
				}
				continue
			}
			var snapID uuid.UUID
			var capturedAt time.Time
			err = tx.QueryRowContext(ctx, `
				INSERT INTO sentry_snapshots (
					org_id, binding_id, component_id, content_hash, environment, service_name,
					sentry_project_id, project_slug, project_name, platform, sentry_org,
					team_id, team_slug, status_filter, generated_at, total_issues,
					score, grade, load, captured_at, collected_at
				) VALUES (
					$1, $2, $3, $4, $5, $6,
					$7, $8, $9, $10, $11,
					$12, $13, $14, $15, $16,
					$17, $18, $19, COALESCE($20::timestamptz, now()), COALESCE($20::timestamptz, now())
				) RETURNING id, captured_at`,
				s.OrgID(), bindingID, snap.ComponentID, snap.ContentHash, snap.Environment, snap.ServiceName,
				snap.SentryProjectID, snap.ProjectSlug, snap.ProjectName, snap.Platform, snap.SentryOrg,
				snap.TeamID, snap.TeamSlug, snap.StatusFilter, snap.GeneratedAt, snap.TotalIssues,
				snap.Score, snap.Grade, snap.Load, atArg).Scan(&snapID, &capturedAt)
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
		if historical {
			return nil // the dashboard's current state is live collection's
		}
		projectID, err := componentProjectID(ctx, tx, componentID)
		if err != nil {
			return err
		}
		return upsertComponentMetricTx(ctx, tx, s.OrgID(), projectID, componentID, "sentry", headline)
	})
}

const sentrySnapshotCols = `id, org_id, binding_id, component_id, captured_at, content_hash, collected_at, environment, service_name,
	sentry_project_id, project_slug, project_name, platform, sentry_org,
	team_id, team_slug, status_filter, generated_at, total_issues, score, grade, load`

// SentrySnapshotsAt is a binding's Sentry state, one snapshot per
// environment. With asOf nil it is the latest sync: exactly the
// environments that sync reported. With asOf set it is what was current
// then — per environment the latest snapshot captured by asOf, among the
// environments still being reported in the day before it.
func SentrySnapshotsAt(ctx context.Context, s tenant.Scope, bindingID uuid.UUID, asOf *time.Time) ([]SentrySnapshot, error) {
	var out []SentrySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if asOf == nil {
			return tx.SelectContext(ctx, &out, `
				SELECT `+sentrySnapshotCols+` FROM sentry_snapshots
				WHERE binding_id = $1
				  AND collected_at = (SELECT max(collected_at) FROM sentry_snapshots WHERE binding_id = $1)
				ORDER BY environment, service_name`, bindingID)
		}
		return tx.SelectContext(ctx, &out, `
			SELECT * FROM (
				SELECT DISTINCT ON (environment, service_name) `+sentrySnapshotCols+` FROM sentry_snapshots
				WHERE binding_id = $1 AND captured_at <= $2
				ORDER BY environment, service_name, captured_at DESC
			) latest
			WHERE collected_at > $2::timestamptz - interval '1 day'
			ORDER BY environment, service_name`, bindingID, *asOf)
	})
	return out, err
}

// SentrySnapshotDatesForBinding is every calendar date (most recent
// first) a Sentry snapshot of this binding was captured on — the date
// picker's options, as for SonarQube.
func SentrySnapshotDatesForBinding(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]string, error) {
	var out []string
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT DISTINCT to_char(captured_at, 'YYYY-MM-DD') FROM sentry_snapshots
			WHERE binding_id = $1 ORDER BY 1 DESC`, bindingID)
	})
	return out, err
}

// ListSentrySnapshotsForBinding is the binding's latest sync, one
// snapshot per environment.
func ListSentrySnapshotsForBinding(ctx context.Context, s tenant.Scope, bindingID uuid.UUID) ([]SentrySnapshot, error) {
	return SentrySnapshotsAt(ctx, s, bindingID, nil)
}

// ListSentrySnapshotsForComponent is the latest sync of every Sentry
// binding on a component — a component with two Sentry bindings (e.g. a
// fanned-out backend split into two Sentry projects mid-migration) shows
// every one of them, not just one.
func ListSentrySnapshotsForComponent(ctx context.Context, s tenant.Scope, componentID uuid.UUID) ([]SentrySnapshot, error) {
	var out []SentrySnapshot
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT `+sentrySnapshotCols+` FROM sentry_snapshots
			WHERE component_id = $1
			  AND (binding_id, collected_at) IN (
				SELECT binding_id, max(collected_at) FROM sentry_snapshots WHERE component_id = $1 GROUP BY binding_id)
			ORDER BY binding_id, environment`, componentID)
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
