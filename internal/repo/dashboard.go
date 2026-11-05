package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

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

// OrgDailyTrends is each key's organization-wide value on each of the last
// `days` calendar days: every live, enabled binding's day-end reading,
// carried forward over days without one exactly as DailySeries does, then
// combined across bindings by combine (keyed by metric key; "sum" adds,
// anything else averages). A day before any binding has a reading has no
// value. It reads every binding at once, so the dashboard's trends cost
// two queries rather than one per binding. projectIDs nil means every
// project; otherwise only bindings of those projects count (the
// dashboard's tag filter).
func OrgDailyTrends(ctx context.Context, s tenant.Scope, combine map[string]string, days int, now time.Time, projectIDs []uuid.UUID) (map[string][]Point, error) {
	if days < 1 {
		days = 1
	}
	keys := make([]string, 0, len(combine))
	for k := range combine {
		keys = append(keys, k)
	}
	type reading struct {
		BindingID uuid.UUID `db:"binding_id"`
		Key       string    `db:"key"`
		Day       time.Time `db:"day"`
		Value     float64   `db:"value"`
	}
	var inWindow, seeds []reading
	const live = `
		FROM metric_bindings mb
		JOIN components c ON c.id = mb.component_id
		JOIN projects p ON p.id = c.project_id
		CROSS JOIN unnest($1::text[]) AS k(key)`
	// projects is the placeholder holding projectIDs, the last argument of
	// each query; NULL keeps every project.
	liveWhere := func(projects string) string {
		return `mb.deleted_at IS NULL AND mb.enabled AND c.archived_at IS NULL AND p.archived_at IS NULL
			AND (` + projects + `::uuid[] IS NULL OR p.id = ANY(` + projects + `))`
	}
	var firstDay time.Time
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &firstDay, `SELECT date_trunc('day', $1::timestamptz) - ($2::int - 1) * interval '1 day'`, now, days); err != nil {
			return err
		}
		// Raw facts win over the rollup for a day both hold, as in DailySeries.
		if err := tx.SelectContext(ctx, &inWindow, `
			SELECT DISTINCT ON (binding_id, key, day) binding_id, key, day, value FROM (
			    SELECT mb.id AS binding_id, k.key, date_trunc('day', f.collected_at) AS day, f.value_num AS value, f.collected_at AS at, 0 AS tier
			    `+live+`
			    JOIN metric_facts f ON f.binding_id = mb.id AND f.key = k.key
			    WHERE `+liveWhere("$4")+` AND f.value_num IS NOT NULL AND f.collected_at >= $2 AND f.collected_at <= $3
			    UNION ALL
			    SELECT mb.id, k.key, d.bucket, d.value_last, d.bucket, 1
			    `+live+`
			    JOIN metric_facts_daily d ON d.binding_id = mb.id AND d.key = k.key
			    WHERE `+liveWhere("$4")+` AND d.value_last IS NOT NULL AND d.bucket >= $2
			) r ORDER BY binding_id, key, day, tier, at DESC`, pq.Array(keys), firstDay, now, projectIDs); err != nil {
			return err
		}
		return tx.SelectContext(ctx, &seeds, `
			SELECT mb.id AS binding_id, k.key, prior.at AS day, prior.value
			`+live+`
			CROSS JOIN LATERAL (
			    SELECT at, value FROM (
			        (SELECT collected_at AS at, value_num AS value FROM metric_facts
			         WHERE binding_id = mb.id AND key = k.key AND value_num IS NOT NULL AND collected_at < $2
			         ORDER BY collected_at DESC LIMIT 1)
			        UNION ALL
			        (SELECT bucket AS at, value_last AS value FROM metric_facts_daily
			         WHERE binding_id = mb.id AND key = k.key AND value_last IS NOT NULL AND bucket < $2
			         ORDER BY bucket DESC LIMIT 1)
			    ) newest ORDER BY at DESC LIMIT 1
			) prior
			WHERE `+liveWhere("$3"), pq.Array(keys), firstDay, projectIDs)
	})
	if err != nil && !isNoRows(err) {
		return nil, err
	}

	type series struct {
		carry *float64
		byDay map[int]float64
	}
	dayIndex := func(t time.Time) int { return int(t.Sub(firstDay).Hours()/24 + 0.5) }
	perKey := map[string]map[uuid.UUID]*series{}
	at := func(key string, id uuid.UUID) *series {
		if perKey[key] == nil {
			perKey[key] = map[uuid.UUID]*series{}
		}
		if perKey[key][id] == nil {
			perKey[key][id] = &series{byDay: map[int]float64{}}
		}
		return perKey[key][id]
	}
	for _, r := range seeds {
		v := r.Value
		at(r.Key, r.BindingID).carry = &v
	}
	for _, r := range inWindow {
		at(r.Key, r.BindingID).byDay[dayIndex(r.Day)] = r.Value
	}

	out := make(map[string][]Point, len(keys))
	for _, key := range keys {
		points := make([]Point, days)
		for i := range points {
			points[i].At = firstDay.AddDate(0, 0, i)
			total, n := 0.0, 0
			for _, b := range perKey[key] {
				if v, ok := b.byDay[i]; ok {
					b.carry = &v
				}
				if b.carry != nil {
					total += *b.carry
					n++
				}
			}
			if n == 0 {
				continue
			}
			if combine[key] != "sum" {
				total /= float64(n)
			}
			points[i].Value, points[i].HasValue, points[i].Min, points[i].Max = total, true, total, total
		}
		out[key] = points
	}
	return out, nil
}
