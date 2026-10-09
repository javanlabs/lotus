package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// The dashboard's queries (internal/web/home_dashboard.go): each is one
// round trip across the organization, RLS-scoped like every other read,
// and limited to live projects and components so an archived one never
// raises an alert or tops a list.

// LiveComponent is a live component with the project it belongs to.
type LiveComponent struct {
	ID          uuid.UUID `db:"id"`
	Name        string    `db:"name"`
	Slug        string    `db:"slug"`
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
	ProjectSlug string    `db:"project_slug"`
}

// ListLiveComponents is every unarchived component of every unarchived
// project, by project then component name.
func ListLiveComponents(ctx context.Context, s tenant.Scope) ([]LiveComponent, error) {
	out := []LiveComponent{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT c.id, c.name, c.slug, p.id AS project_id, p.name AS project_name, p.slug AS project_slug
			FROM components c JOIN projects p ON p.id = c.project_id
			WHERE c.archived_at IS NULL AND p.archived_at IS NULL
			ORDER BY lower(p.name), lower(c.name), c.id`)
	})
	return out, err
}

// FailingBinding is a live, enabled binding whose recent syncs keep
// failing, with where it sits.
type FailingBinding struct {
	BindingID           uuid.UUID `db:"binding_id"`
	Kind                string    `db:"kind"`
	ConsecutiveFailures int       `db:"consecutive_failures"`
	LiveComponent
}

// ListFailingBindings is every live, enabled binding with at least
// minFailures consecutive failed syncs, the most failures first.
func ListFailingBindings(ctx context.Context, s tenant.Scope, minFailures int) ([]FailingBinding, error) {
	out := []FailingBinding{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT mb.id AS binding_id, mb.kind, mb.consecutive_failures,
			       c.id, c.name, c.slug, p.id AS project_id, p.name AS project_name, p.slug AS project_slug
			FROM metric_bindings mb
			JOIN components c ON c.id = mb.component_id
			JOIN projects p ON p.id = c.project_id
			WHERE mb.deleted_at IS NULL AND mb.enabled AND mb.consecutive_failures >= $1
			  AND c.archived_at IS NULL AND p.archived_at IS NULL
			ORDER BY mb.consecutive_failures DESC, lower(p.name), lower(c.name), mb.kind`, minFailures)
	})
	return out, err
}

// CountFailedSyncsSince is how many syncs of the organization's bindings
// ended failed or partial since since.
func CountFailedSyncsSince(ctx context.Context, s tenant.Scope, since time.Time) (int, error) {
	var n int
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &n, `
			SELECT count(*) FROM sync_runs
			WHERE org_id = $1 AND status IN ('failed', 'partial') AND finished_at >= $2`, s.OrgID(), since)
	})
	return n, err
}

// QAIssueSummary is an issue with its project, for lists across projects.
type QAIssueSummary struct {
	IssueID     uuid.UUID `db:"issue_id"`
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
	ProjectSlug string    `db:"project_slug"`
	Key         int       `db:"key"`
	Title       string    `db:"title"`
	Severity    string    `db:"severity"`
	Status      string    `db:"status"`
	CreatedAt   time.Time `db:"created_at"`
}

// ListOpenQAIssuesBySeverity is every open issue (QAOpenIssueStatuses) of
// one of severities on a live project, newest first.
func ListOpenQAIssuesBySeverity(ctx context.Context, s tenant.Scope, severities []string) ([]QAIssueSummary, error) {
	out := []QAIssueSummary{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			SELECT i.id AS issue_id, p.id AS project_id, p.name AS project_name, p.slug AS project_slug,
			       i.key, i.title, i.severity, i.status, i.created_at
			FROM qa_issues i JOIN projects p ON p.id = i.project_id
			WHERE p.archived_at IS NULL AND i.status = ANY($1) AND i.severity = ANY($2)
			ORDER BY i.created_at DESC, i.id`, QAOpenIssueStatuses, severities)
	})
	return out, err
}

// QAIssueActivity is one thing that happened to an issue: it was reported
// (Kind "reported"), its status changed ("status"), or it was commented
// on ("comment"). Actor is the person's name, "" when unknown.
type QAIssueActivity struct {
	At         time.Time `db:"at"`
	Kind       string    `db:"kind"`
	StatusFrom *string   `db:"status_from"`
	StatusTo   *string   `db:"status_to"`
	Actor      string    `db:"actor"`
	QAIssueSummary
}

// ListRecentQAIssueActivity is the organization's last limit issue
// happenings on live projects, newest first. A report is the issue's own
// row, not an event: events record only what changes after it.
func ListRecentQAIssueActivity(ctx context.Context, s tenant.Scope, limit int) ([]QAIssueActivity, error) {
	out := []QAIssueActivity{}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `
			WITH happened AS (
				SELECT i.id AS issue_id, i.created_at AS at, 'reported' AS kind,
				       NULL::text AS status_from, NULL::text AS status_to, i.reported_by AS actor_id
				FROM qa_issues i
				UNION ALL
				SELECT e.issue_id, e.created_at, e.kind, e.status_from, e.status_to, e.actor_id
				FROM qa_issue_events e
			)
			SELECT h.at, h.kind, h.status_from, h.status_to,
			       COALESCE(NULLIF(btrim(u.name), ''), u.email, '') AS actor,
			       i.id AS issue_id, p.id AS project_id, p.name AS project_name, p.slug AS project_slug,
			       i.key, i.title, i.severity, i.status, i.created_at
			FROM happened h
			JOIN qa_issues i ON i.id = h.issue_id
			JOIN projects p ON p.id = i.project_id
			LEFT JOIN users u ON u.id = h.actor_id
			WHERE p.archived_at IS NULL
			ORDER BY h.at DESC, i.id
			LIMIT $1`, limit)
	})
	return out, err
}
