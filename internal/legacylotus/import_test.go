package legacylotus_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	lotusdb "github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/legacylotus"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

type fixture struct {
	app, ddl                *sqlx.DB
	s                       tenant.Scope
	bpm, auth               uuid.UUID // components
	sonarBinding, glBinding uuid.UUID
}

// newFixture is lotus.javan.id after seed-connections and seed-services:
// components carrying their legacy service name, and the sonar and gitlab
// bindings seed-services creates. No trivy binding: the import makes them.
func newFixture(t *testing.T) fixture {
	t.Helper()
	dsn := dbtest.DSN(t)
	ddl, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ddl.Close() })
	app, err := lotusdb.OpenApp(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	var orgID uuid.UUID
	if err := app.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	f := fixture{app: app, ddl: ddl, s: tenant.FromRun(app, orgID)}
	if err := f.s.Tx(context.Background(), func(tx *sqlx.Tx) error {
		var projectID, sonarConn, glConn uuid.UUID
		steps := []struct {
			dst *uuid.UUID
			q   string
			a   []any
		}{
			{&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'alurkerja', 'AlurKerja') RETURNING id`, []any{orgID}},
			{&f.bpm, `INSERT INTO components (org_id, project_id, slug, name, kind, legacy_name) VALUES ($1, $2, 'bpm-service', 'bpm-service', 'backend', 'bpm-service') RETURNING id`, nil},
			{&f.auth, `INSERT INTO components (org_id, project_id, slug, name, kind, legacy_name) VALUES ($1, $2, 'authentication-service', 'authentication-service', 'backend', 'authentication-service') RETURNING id`, nil},
			{&sonarConn, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'sonarqube', 'SonarQube', 'https://sonar.example') RETURNING id`, []any{orgID}},
			{&glConn, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'GitLab', 'https://gitlab.example') RETURNING id`, []any{orgID}},
			{nil, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'Harbor', 'https://harbor.example') RETURNING id`, []any{orgID}},
		}
		for i, st := range steps {
			args := st.a
			if args == nil {
				args = []any{orgID, projectID}
			}
			var sink uuid.UUID
			dst := st.dst
			if dst == nil {
				dst = &sink
			}
			if err := tx.Get(dst, st.q, args...); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
		}
		if err := tx.Get(&f.sonarBinding, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, 'sonarqube', 'alurkerja-bpm') RETURNING id`, orgID, f.bpm, sonarConn); err != nil {
			return err
		}
		return tx.Get(&f.glBinding, `INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key) VALUES ($1, $2, $3, 'gitlab', 'alurkerja/bpm') RETURNING id`, orgID, f.bpm, glConn)
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// export is what deploy/legacy-lotus-export.py writes: one JSON object per
// line, oldest first.
const export = `{"kind":"meta","exported_at":"2026-09-28T13:10:00Z"}
{"kind":"sonar","service":"bpm-service","captured_at":"2026-05-05 03:58:48.413122189+00:00","quality_gate":"OK","coverage":61.5,"bugs":3,"code_smells":40,"reliability_rating":"C"}
{"kind":"sonar","service":"bpm-service","captured_at":"2026-05-05 04:08:48+00:00","quality_gate":"OK","coverage":61.5,"bugs":3,"code_smells":40,"reliability_rating":"C"}
{"kind":"gitlab","service":"bpm-service","captured_at":"2026-05-25T03:32:01.941177039Z","default_branch":"main","branches_total":12,"branches_stale":4,"commits_30d":31,"last_pipeline_status":"success","pipeline_success_rate":0.9,"pipeline_sample_size":20,"default_branch_protected":1,"archived":0}
{"kind":"trivy","repo":"alurkerja-v2/alurkerja-bpm-release","image":"alurkerja-bpm-release","captured_at":"2026-06-02 05:57:39+00:00","digest":"sha256:abc","tags":"1.4.0,latest","severity":"High","total":9,"fixable":4,"critical":0,"high":2,"medium":5,"low":2,"unknown":0,"pulls":7,"artifact_count":3}
{"kind":"sonar","service":"bpm-service","captured_at":"2026-07-01 00:00:00+00:00","quality_gate":"ERROR","coverage":58.0,"bugs":5,"code_smells":44,"reliability_rating":"D"}
{"kind":"sonar","service":"gone-service","captured_at":"2026-07-01 00:00:00+00:00","coverage":10}
{"kind":"trivy","repo":"alurkerja-v2/some-tool","image":"some-tool","captured_at":"2026-07-01 00:00:00+00:00","total":1}
`

func count(t *testing.T, f fixture, q string, a ...any) int {
	t.Helper()
	var n int
	if err := f.s.Tx(context.Background(), func(tx *sqlx.Tx) error { return tx.Get(&n, q, a...) }); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestImport_HistoryLandsAtItsOwnTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	dry, err := legacylotus.Import(ctx, f.app, f.ddl, f.s.OrgID(), strings.NewReader(export), legacylotus.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	// A dry run reports what it would import, and writes nothing.
	if dry.Read != 7 || dry.Imported != 5 || count(t, f, `SELECT count(*) FROM sonar_snapshots`) != 0 ||
		count(t, f, `SELECT count(*) FROM metric_bindings WHERE kind = 'trivy'`) != 0 {
		t.Fatalf("dry run = %+v; want 7 read, 5 importable, nothing written", dry)
	}

	rep, err := legacylotus.Import(ctx, f.app, f.ddl, f.s.OrgID(), strings.NewReader(export), legacylotus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Imported != 5 || len(rep.UnknownServices) != 1 || rep.UnknownServices[0] != "gone-service" ||
		len(rep.UnknownImages) != 1 || rep.UnknownImages[0] != "some-tool" {
		t.Fatalf("report = %+v", rep)
	}

	// Sonar: the two identical May snapshots are one row (seen twice), July
	// its own; all at their own time.
	var sonar []time.Time
	if err := f.s.Tx(ctx, func(tx *sqlx.Tx) error {
		return tx.Select(&sonar, `SELECT captured_at FROM sonar_snapshots WHERE binding_id = $1 ORDER BY captured_at`, f.sonarBinding)
	}); err != nil {
		t.Fatal(err)
	}
	may := time.Date(2026, 5, 5, 3, 58, 48, 413122000, time.UTC)
	if len(sonar) != 2 || !sonar[0].Equal(may) || sonar[1].Month() != time.July {
		t.Fatalf("sonar snapshots at %v", sonar)
	}
	if n := count(t, f, `SELECT count(*) FROM gitlab_snapshots WHERE binding_id = $1 AND branches_stale = 4 AND default_branch_protected`, f.glBinding); n != 1 {
		t.Errorf("gitlab snapshots = %d", n)
	}
	// Trivy: the binding is made for the Harbor repository, on the
	// component its image maps to.
	if n := count(t, f, `SELECT count(*) FROM trivy_snapshots s JOIN metric_bindings b ON b.id = s.binding_id
		WHERE b.component_id = $1 AND b.external_key = 'alurkerja-v2/alurkerja-bpm-release' AND s.high = 2 AND s.tags = '{1.4.0,latest}'`, f.bpm); n != 1 {
		t.Errorf("trivy snapshots = %d", n)
	}
	// Facts and the daily rollup the long-range charts read.
	if n := count(t, f, `SELECT count(*) FROM metric_facts WHERE binding_id = $1 AND key = 'sonar.coverage'`, f.sonarBinding); n != 3 {
		t.Errorf("coverage facts = %d; want 3 (one per imported observation)", n)
	}
	if n := count(t, f, `SELECT count(*) FROM metric_facts_daily WHERE binding_id = $1 AND key = 'sonar.coverage'`, f.sonarBinding); n != 2 {
		t.Errorf("daily coverage buckets = %d; want 2 (5 May, 1 July)", n)
	}
	// The dashboard's current state is left to live collection.
	if n := count(t, f, `SELECT count(*) FROM component_metrics_current`); n != 0 {
		t.Errorf("current-state rows = %d; the import must not write them", n)
	}

	// Running it again (an interrupted import resumed) adds nothing.
	facts, snaps := count(t, f, `SELECT count(*) FROM metric_facts`), count(t, f, `SELECT count(*) FROM sonar_snapshots`)
	again, err := legacylotus.Import(ctx, f.app, f.ddl, f.s.OrgID(), strings.NewReader(export), legacylotus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Imported != 0 || again.AlreadyImported != 5 ||
		count(t, f, `SELECT count(*) FROM metric_facts`) != facts || count(t, f, `SELECT count(*) FROM sonar_snapshots`) != snaps {
		t.Errorf("second run = %+v; want nothing imported twice", again)
	}
}

// Live collection starts as soon as the bindings exist, possibly before
// the import runs. Its rows (after the export time) neither stop the
// history from being imported nor change.
func TestImport_AlongsideLiveCollection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cov := 77.0
	if _, _, err := repo.InsertSonarSnapshot(ctx, f.s, repo.NewSonarSnapshot{BindingID: f.sonarBinding, ComponentID: f.bpm, Coverage: &cov},
		"live", collector.Headline{Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	rep, err := legacylotus.Import(ctx, f.app, f.ddl, f.s.OrgID(), strings.NewReader(export), legacylotus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Imported != 5 {
		t.Fatalf("report = %+v; want the history imported beside the live row", rep)
	}
	if n := count(t, f, `SELECT count(*) FROM sonar_snapshots WHERE binding_id = $1 AND coverage = 77 AND collected_at > now() - interval '1 hour'`, f.sonarBinding); n != 1 {
		t.Errorf("live row = %d; want it untouched", n)
	}
	if n := count(t, f, `SELECT count(*) FROM component_metrics_current WHERE component_id = $1`, f.bpm); n != 1 {
		t.Errorf("current state rows = %d; want the live one", n)
	}
}
