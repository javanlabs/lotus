package reproject_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/reproject"
	"github.com/uyab/lotus/internal/tenant"
)

func inOrg(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A component stored under the old rules — GitLab scored from branches
// alone with no pipelines, Trivy uncapped at 55, SonarQube truncated, the
// component worst-wins — is rebuilt from its snapshots to what a collect
// would write today, keeps each kind's last-synced time, and has today's
// status day rewritten with it.
func TestBuildAndApply_RebuildsFromStoredSnapshots(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	var orgID, projectID, componentID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme-reproject', 'Acme') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	synced := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	old := map[string]repo.KindHeadline{
		"sonarqube": {Status: "fail", Score: ptr(47), Grade: "A", Summary: "ERROR", UpdatedAt: &synced},
		"trivy":     {Status: "fail", Score: ptr(55), Summary: "1 critical, 5 high, 31 total", UpdatedAt: &synced},
		"gitlab":    {Status: "unknown", Score: ptr(93), Summary: "no pipeline data", UpdatedAt: &synced},
	}
	oldJSON, _ := json.Marshal(old)
	inOrg(t, db, orgID, func(tx *sqlx.Tx) {
		must := func(dst *uuid.UUID, q string, a ...any) {
			t.Helper()
			if err := tx.Get(dst, q, a...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		must(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'ak', 'AlurKerja') RETURNING id`, orgID)
		must(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'camunda', 'camunda-bpm-platform', 'backend') RETURNING id`, orgID, projectID)
		bind := func(kind, provider string) uuid.UUID {
			var conn, b uuid.UUID
			must(&conn, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, $2, $2, 'https://x.example') RETURNING id`, orgID, provider)
			must(&b, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, $4, 'k') RETURNING id`, orgID, componentID, conn, kind)
			return b
		}
		sonarB, trivyB, gitlabB := bind("sonarqube", "sonarqube"), bind("trivy", "harbor_trivy"), bind("gitlab", "gitlab")
		exec := func(q string, a ...any) {
			t.Helper()
			if _, err := tx.Exec(q, a...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		exec(`INSERT INTO sonar_snapshots (org_id, binding_id, component_id, quality_gate, coverage, reliability_rating, raw_json)
			VALUES ($1, $2, $3, 'ERROR', 89.6, 'A', '{"QualityGate":"ERROR","Coverage":89.6,"ReliabilityRating":"A","Bugs":0}')`, orgID, sonarB, componentID)
		exec(`INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, total, critical, high, scan_finished_at)
			VALUES ($1, $2, $3, 'registry', 31, 1, 5, now())`, orgID, trivyB, componentID)
		exec(`INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, branches_total, branches_active, branches_stale, commits_30d, raw_json)
			VALUES ($1, $2, $3, 30, 27, 3, 4, '{"BranchesTotal":30,"BranchesActive":27,"BranchesStale":3,"Commits30d":4}')`, orgID, gitlabB, componentID)
		exec(`INSERT INTO component_metrics_current (org_id, project_id, component_id, metrics, status, score, updated_at)
			VALUES ($1, $2, $3, $4, 'fail', 65, now())`, orgID, projectID, componentID, oldJSON)
	})

	s := tenant.FromRun(db, orgID)
	plan, err := reproject.Build(ctx, s, "critical")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Projects) != 1 || len(plan.Projects[0].Components) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	c := plan.Projects[0].Components[0]
	// SonarQube (89.6 + 100) / 2 × 0.5 = 47.4 → 47; Trivy capped at 49;
	// GitLab has no pipelines and no score. (47 + 49) / 2 = 48.
	if c.AfterScore == nil || *c.AfterScore != 48 || c.AfterStatus != collector.StatusFail {
		t.Fatalf("component after = %v %q", c.AfterScore, c.AfterStatus)
	}
	// Build only reads.
	before, err := repo.GetComponentMetric(ctx, s, componentID)
	if err != nil || before.Score == nil || *before.Score != 65 {
		t.Fatalf("Build wrote: %+v %v", before, err)
	}

	n, err := reproject.Apply(ctx, s, plan)
	if err != nil || n != 1 {
		t.Fatalf("Apply = %d, %v", n, err)
	}
	got, err := repo.GetComponentMetric(ctx, s, componentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Score == nil || *got.Score != 48 || got.Status != "fail" {
		t.Fatalf("row = %v %q, want 48 fail", got.Score, got.Status)
	}
	kinds := got.Metrics()
	if g := kinds["gitlab"]; g.Score != nil || g.StateLabel != "No pipeline data" {
		t.Errorf("gitlab = %+v, want no score", g)
	}
	if tr := kinds["trivy"]; tr.Score == nil || *tr.Score != 49 {
		t.Errorf("trivy = %+v, want 49", tr)
	}
	for k, kh := range kinds {
		if kh.UpdatedAt == nil || !kh.UpdatedAt.Equal(synced) {
			t.Errorf("%s updated_at = %v, want the last sync kept", k, kh.UpdatedAt)
		}
	}
	days, err := repo.ProjectStatusDays(ctx, s, projectID, time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1))
	if err != nil || len(days) != 1 || days[0].Score == nil || *days[0].Score != 48 || days[0].Status != "fail" {
		t.Fatalf("status day = %+v, %v", days, err)
	}
}

func ptr(v int) *int { return &v }
