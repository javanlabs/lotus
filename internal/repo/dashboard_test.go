package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func TestOrgDailyTrends_CarriesForwardAndCombinesAcrossBindings(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, componentID, first := seedMFFixture(t, db, "acme-org-trends", "gitlab")
	scope := tenant.FromRun(db, orgID)

	var connID, second, deleted string
	var today time.Time
	if err := db.Get(&today, `SELECT date_trunc('day', now())`); err != nil {
		t.Fatal(err)
	}
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&connID, `SELECT connection_id FROM metric_bindings WHERE id = $1`, first); err != nil {
			t.Fatal(err)
		}
		// One binding per kind per component, so each extra binding needs
		// its own component.
		var projectID string
		if err := tx.Get(&projectID, `SELECT project_id FROM components WHERE id = $1`, componentID); err != nil {
			t.Fatal(err)
		}
		for name, dest := range map[string]*string{"second": &second, "deleted": &deleted} {
			var comp string
			if err := tx.Get(&comp, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,$3,$3,'backend') RETURNING id`, orgID, projectID, name); err != nil {
				t.Fatal(err)
			}
			if err := tx.Get(dest, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config)
				VALUES ($1,$2,$3,'gitlab',$4,'{}') RETURNING id`, orgID, comp, connID, name); err != nil {
				t.Fatal(err)
			}
		}
		raw := func(binding any, at time.Time, v float64) {
			if _, err := tx.Exec(`INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
				SELECT $1, id, component_id, 'k', $3::float8, '%', $4 FROM metric_bindings WHERE id = $2`, orgID, binding, v, at); err != nil {
				t.Fatal(err)
			}
		}
		raw(first, today.AddDate(0, 0, -10).Add(time.Hour), 10) // before the window
		raw(first, today.AddDate(0, 0, -1).Add(time.Hour), 20)
		raw(first, today.AddDate(0, 0, -1).Add(5*time.Hour), 30) // the day's last wins
		raw(second, today.AddDate(0, 0, -1).Add(time.Hour), 50)
		raw(deleted, today.AddDate(0, 0, -1).Add(time.Hour), 1000)
		if _, err := tx.Exec(`UPDATE metric_bindings SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
			t.Fatal(err)
		}
	})

	now := today.Add(12 * time.Hour)
	for combine, want := range map[string][3]float64{
		"avg": {10, 40, 40},
		"sum": {10, 80, 80},
	} {
		got, err := repo.OrgDailyTrends(context.Background(), scope, map[string]string{"k": combine}, 3, now, nil)
		if err != nil {
			t.Fatalf("OrgDailyTrends: %v", err)
		}
		points := got["k"]
		if len(points) != 3 {
			t.Fatalf("%s: %d points", combine, len(points))
		}
		for i, p := range points {
			if !p.HasValue || p.Value != want[i] {
				t.Errorf("%s day %d = %+v, want %v", combine, i, p, want[i])
			}
		}
	}
}

func TestOrgDailyTrends_NoReadingsMeansNoValues(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, _, _ := seedMFFixture(t, db, "acme-org-trends-empty", "gitlab")
	got, err := repo.OrgDailyTrends(context.Background(), tenant.FromRun(db, orgID), map[string]string{"k": "avg"}, 3, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got["k"] {
		if p.HasValue {
			t.Errorf("point = %+v", p)
		}
	}
}

// The dashboard's tag filter narrows the trends to the chosen projects;
// nil keeps every project.
func TestOrgDailyTrends_NarrowsToTheGivenProjects(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, componentID, first := seedMFFixture(t, db, "acme-org-trends-narrow", "gitlab")
	scope := tenant.FromRun(db, orgID)

	var today time.Time
	if err := db.Get(&today, `SELECT date_trunc('day', now())`); err != nil {
		t.Fatal(err)
	}
	var firstProject, secondProject uuid.UUID
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		var connID uuid.UUID
		if err := tx.Get(&connID, `SELECT connection_id FROM metric_bindings WHERE id = $1`, first); err != nil {
			t.Fatal(err)
		}
		if err := tx.Get(&firstProject, `SELECT project_id FROM components WHERE id = $1`, componentID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Get(&secondProject, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'q', 'Q') RETURNING id`, orgID); err != nil {
			t.Fatal(err)
		}
		var comp, second uuid.UUID
		if err := tx.Get(&comp, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1,$2,'c2','C2','backend') RETURNING id`, orgID, secondProject); err != nil {
			t.Fatal(err)
		}
		if err := tx.Get(&second, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1,$2,$3,'gitlab','y') RETURNING id`, orgID, comp, connID); err != nil {
			t.Fatal(err)
		}
		for binding, v := range map[uuid.UUID]float64{first: 10, second: 30} {
			if _, err := tx.Exec(`INSERT INTO metric_facts (org_id, binding_id, component_id, key, value_num, unit, collected_at)
				SELECT $1, id, component_id, 'k', $3::float8, '%', $4 FROM metric_bindings WHERE id = $2`,
				orgID, binding, v, today.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	})

	now := today.Add(12 * time.Hour)
	lastValue := func(projectIDs []uuid.UUID) float64 {
		t.Helper()
		got, err := repo.OrgDailyTrends(context.Background(), scope, map[string]string{"k": "sum"}, 1, now, projectIDs)
		if err != nil {
			t.Fatal(err)
		}
		p := got["k"][0]
		if !p.HasValue {
			t.Fatalf("projects %v: no value", projectIDs)
		}
		return p.Value
	}
	if got := lastValue([]uuid.UUID{secondProject}); got != 30 {
		t.Errorf("narrowed to the second project = %v, want 30", got)
	}
	if got := lastValue([]uuid.UUID{firstProject}); got != 10 {
		t.Errorf("narrowed to the first project = %v, want 10", got)
	}
	if got := lastValue(nil); got != 40 {
		t.Errorf("every project = %v, want 40", got)
	}
}
