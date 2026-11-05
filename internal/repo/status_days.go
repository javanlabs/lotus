package repo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/tenant"
)

// A project's status per WIB day, for the public status page
// (docs/superpowers/specs/2026-11-03-tags-and-status-page-design.md §6.4–6.5).
// Today's row is rewritten in the same transaction that changes the
// dashboard projection, so the page can never disagree with what the
// signed-in project page showed that day; once the day is over its row is
// never touched again, so a report sent to a client reads the same when
// reopened.

// StatusDayComponent is one live component as it stood that day. Its
// headlines carry only what the public page may show: status and the
// headline value. Summaries and links stay inside the app.
type StatusDayComponent struct {
	Name  string                  `json:"name"`
	Kinds map[string]KindHeadline `json:"kinds"`
}

// ProjectStatusDay is one recorded day.
type ProjectStatusDay struct {
	Day        time.Time `db:"day"`
	Status     string    `db:"status"`
	Score      *int      `db:"score"`
	Components []StatusDayComponent
	UpdatedAt  time.Time `db:"updated_at"`
}

// publicHeadline keeps the fields a client may see.
func publicHeadline(kh KindHeadline) KindHeadline {
	return KindHeadline{
		Status: kh.Status, Score: kh.Score, Grade: kh.Grade,
		CountValue: kh.CountValue, CountLabel: kh.CountLabel, StateLabel: kh.StateLabel,
	}
}

// recordProjectDayTx rewrites projectID's row for today (WIB) from the
// projection of its live components, rolled up as the project page rolls
// them up. A project with nothing measured left reads unknown.
func recordProjectDayTx(ctx context.Context, tx *sqlx.Tx, projectID uuid.UUID) error {
	// Two syncs of one project's components commit in some order; locking
	// the project makes the second wait and then read the first's write,
	// so the last row written reflects both.
	var orgID uuid.UUID
	if err := tx.GetContext(ctx, &orgID, `SELECT org_id FROM projects WHERE id = $1 FOR NO KEY UPDATE`, projectID); err != nil {
		return err
	}
	var rows []struct {
		Name string `db:"name"`
		ComponentMetric
	}
	if err := tx.SelectContext(ctx, &rows, `
		SELECT c.name, m.org_id, m.project_id, m.component_id, m.status, m.score, m.metrics, m.updated_at
		FROM component_metrics_current m
		JOIN components c ON c.id = m.component_id
		WHERE c.project_id = $1 AND c.archived_at IS NULL
		ORDER BY lower(c.name), c.name, c.id`, projectID); err != nil {
		return err
	}

	metrics := make([]ComponentMetric, 0, len(rows))
	components := make([]StatusDayComponent, 0, len(rows))
	for _, r := range rows {
		metrics = append(metrics, r.ComponentMetric)
		kinds := map[string]KindHeadline{}
		for kind, kh := range r.Metrics() {
			kinds[kind] = publicHeadline(kh)
		}
		components = append(components, StatusDayComponent{Name: r.Name, Kinds: kinds})
	}
	roll := RollupProject(metrics)
	componentsJSON, err := json.Marshal(components)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO project_status_days (org_id, project_id, day, status, score, components, updated_at)
		VALUES ($1, $2, (now() AT TIME ZONE 'Asia/Jakarta')::date, $3, $4, $5, now())
		ON CONFLICT (project_id, day) DO UPDATE SET
			status = EXCLUDED.status, score = EXCLUDED.score,
			components = EXCLUDED.components, updated_at = EXCLUDED.updated_at`,
		orgID, projectID, string(roll.Status), roll.Score, componentsJSON)
	return err
}

// ProjectStatusDays is projectID's recorded days from `from` to `to`
// inclusive (dates taken in each time's own location), plus the newest
// day before `from` when there is one: a day with no row of its own
// carries the last recorded one forward, and the first day of the range
// needs that seed. Ascending by day.
func ProjectStatusDays(ctx context.Context, s tenant.Scope, projectID uuid.UUID, from, to time.Time) ([]ProjectStatusDay, error) {
	var rows []struct {
		Day            time.Time `db:"day"`
		Status         string    `db:"status"`
		Score          *int      `db:"score"`
		ComponentsJSON []byte    `db:"components"`
		UpdatedAt      time.Time `db:"updated_at"`
	}
	err := s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT day, status, score, components, updated_at FROM (
				SELECT day, status, score, components, updated_at FROM project_status_days
				WHERE project_id = $1 AND day BETWEEN $2::date AND $3::date
				UNION ALL
				(SELECT day, status, score, components, updated_at FROM project_status_days
				 WHERE project_id = $1 AND day < $2::date
				 ORDER BY day DESC LIMIT 1)
			) d ORDER BY day`,
			projectID, from.Format(time.DateOnly), to.Format(time.DateOnly))
	})
	if err != nil {
		return nil, err
	}
	out := make([]ProjectStatusDay, 0, len(rows))
	for _, r := range rows {
		components := []StatusDayComponent{}
		if err := json.Unmarshal(r.ComponentsJSON, &components); err != nil {
			return nil, err
		}
		out = append(out, ProjectStatusDay{
			Day: r.Day, Status: r.Status, Score: r.Score, Components: components, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// SetProjectStatusPage publishes (on) or withdraws a live project's
// public status page. A project this organization cannot see is
// ErrNotFound.
func SetProjectStatusPage(ctx context.Context, s tenant.Scope, id uuid.UUID, on bool) error {
	return s.Tx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE projects SET status_page_enabled_at = CASE WHEN $2 THEN now() END
			WHERE id = $1 AND deleted_at IS NULL`, id, on)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}
