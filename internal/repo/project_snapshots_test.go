package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// projectKindFixture is one organization's project with two components,
// a second project, and one connection per kind — what the project-level
// latest-snapshot reads are tested against.
type projectKindFixture struct {
	orgID, projectID, otherProjectID uuid.UUID
	compA, compB, otherComp          uuid.UUID
	conns                            map[string]uuid.UUID
}

func seedProjectKindFixture(t *testing.T, db *sqlx.DB, subdomain string) projectKindFixture {
	t.Helper()
	f := projectKindFixture{conns: map[string]uuid.UUID{}}
	if err := db.Get(&f.orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxCM(t, db, f.orgID, func(tx *sqlx.Tx) {
		mustGet := func(dst *uuid.UUID, q string, args ...any) {
			t.Helper()
			if err := tx.Get(dst, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		mustGet(&f.projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, f.orgID)
		mustGet(&f.otherProjectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'q', 'Q') RETURNING id`, f.orgID)
		mustGet(&f.compA, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'a', 'A', 'backend') RETURNING id`, f.orgID, f.projectID)
		mustGet(&f.compB, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'b', 'B', 'backend') RETURNING id`, f.orgID, f.projectID)
		mustGet(&f.otherComp, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'o', 'O', 'backend') RETURNING id`, f.orgID, f.otherProjectID)
		for kind, provider := range map[string]string{"sonarqube": "sonarqube", "gitlab": "gitlab", "trivy": "harbor_trivy", "sentry": "sentry"} {
			var id uuid.UUID
			mustGet(&id, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, $2, $2, 'https://'||$2||'.example.com') RETURNING id`, f.orgID, provider)
			f.conns[kind] = id
		}
	})
	return f
}

// bind inserts a binding of kind on component; retired bindings are
// soft-deleted at once.
func (f projectKindFixture) bind(t *testing.T, db *sqlx.DB, component uuid.UUID, kind, key string, retired bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	withOrgTxCM(t, db, f.orgID, func(tx *sqlx.Tx) {
		if err := tx.Get(&id, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, config, deleted_at)
			VALUES ($1, $2, $3, $4, $5, '{}', CASE WHEN $6 THEN now() END) RETURNING id`,
			f.orgID, component, f.conns[kind], kind, key, retired); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return id
}

func (f projectKindFixture) exec(t *testing.T, db *sqlx.DB, q string, args ...any) {
	t.Helper()
	withOrgTxCM(t, db, f.orgID, func(tx *sqlx.Tx) {
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	})
}

// The project reads see the latest snapshot of each live binding of the
// project and nothing else: not an older snapshot of the same binding, not
// a retired binding's newer one, not another project's, and not another
// organization's.
func TestListLatestSonarSnapshotsForProject_LatestOfLiveBindingsOnly(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	f := seedProjectKindFixture(t, db, "acme-psnap")
	other := seedProjectKindFixture(t, db, "acme-psnap-other")
	s := tenant.FromRun(db, f.orgID)

	retired := f.bind(t, db, f.compA, "sonarqube", "old-key", true)
	live := f.bind(t, db, f.compA, "sonarqube", "a-key", false)
	f.bind(t, db, f.compB, "sonarqube", "b-key", false) // bound, never synced
	elsewhere := f.bind(t, db, f.otherComp, "sonarqube", "o-key", false)
	ins := `INSERT INTO sonar_snapshots (org_id, binding_id, component_id, coverage, collected_at)
	        VALUES ($1, $2, $3, $4, now() - $5::interval)`
	f.exec(t, db, ins, f.orgID, live, f.compA, 10.0, "2 days")
	f.exec(t, db, ins, f.orgID, live, f.compA, 80.0, "1 hour")
	f.exec(t, db, ins, f.orgID, retired, f.compA, 99.0, "1 minute")
	f.exec(t, db, ins, f.orgID, elsewhere, f.otherComp, 50.0, "1 minute")
	otherLive := other.bind(t, db, other.compA, "sonarqube", "x", false)
	other.exec(t, db, ins, other.orgID, otherLive, other.compA, 1.0, "1 minute")

	got, err := repo.ListLatestSonarSnapshotsForProject(ctx, s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BindingID != live || got[0].Coverage == nil || *got[0].Coverage != 80 {
		t.Fatalf("got %+v, want only the live binding's latest snapshot (80%%)", got)
	}

	// Another organization's project, asked for by id, reads as empty.
	got, err = repo.ListLatestSonarSnapshotsForProject(ctx, s, other.projectID)
	if err != nil || len(got) != 0 {
		t.Fatalf("other org's project = %+v, %v; want nothing", got, err)
	}

	bindings, err := repo.ListProjectBindingsByKind(ctx, s, f.projectID, "sonarqube")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].ComponentID != f.compA || bindings[1].ComponentID != f.compB {
		t.Fatalf("bindings = %+v, want A's live one and B's", bindings)
	}
	if bindings[0].BindingID != live || bindings[0].ConnectionURL != "https://sonarqube.example.com" {
		t.Fatalf("binding A = %+v", bindings[0])
	}
}

func TestListLatestGitlabAndTrivySnapshotsForProject(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	f := seedProjectKindFixture(t, db, "acme-psnap-gt")
	s := tenant.FromRun(db, f.orgID)

	gl := f.bind(t, db, f.compA, "gitlab", "grp/a", false)
	glRetired := f.bind(t, db, f.compB, "gitlab", "grp/b", true)
	f.exec(t, db, `INSERT INTO gitlab_snapshots (org_id, binding_id, component_id, default_branch, collected_at) VALUES
		($1, $2, $3, 'old', now() - interval '1 day'), ($1, $2, $3, 'main', now()), ($1, $4, $5, 'gone', now())`,
		f.orgID, gl, f.compA, glRetired, f.compB)
	gls, err := repo.ListLatestGitlabSnapshotsForProject(ctx, s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gls) != 1 || gls[0].DefaultBranch == nil || *gls[0].DefaultBranch != "main" {
		t.Fatalf("gitlab = %+v, want A's latest (main) only", gls)
	}

	tv := f.bind(t, db, f.compA, "trivy", "proj/a", false)
	f.exec(t, db, `INSERT INTO trivy_snapshots (org_id, binding_id, component_id, source, total, critical, raw_json, collected_at) VALUES
		($1, $2, $3, 'registry', 9, 9, '{"vulnerabilities":[]}', now() - interval '1 day'),
		($1, $2, $3, 'registry', 2, 1, '{"vulnerabilities":[]}', now())`, f.orgID, tv, f.compA)
	tvs, err := repo.ListLatestTrivySnapshotsForProject(ctx, s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tvs) != 1 || tvs[0].Critical != 1 || len(tvs[0].RawJSON) != 0 {
		t.Fatalf("trivy = %+v, want the latest counts without the raw report", tvs)
	}

	f.exec(t, db, `INSERT INTO trivy_finding_notes (org_id, binding_id, cve_id, package, status, comment) VALUES
		($1, $2, 'CVE-1', 'a', 'false_positive', 'x'), ($1, $2, 'CVE-2', 'b', 'false_positive', 'y'),
		($1, $2, 'CVE-3', 'c', 'wont_fix', 'z')`, f.orgID, tv)
	fps, err := repo.CountFalsePositivesForProject(ctx, s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fps) != 1 || fps[tv] != 2 {
		t.Fatalf("false positives = %v, want %s: 2", fps, tv)
	}
}

// Sentry's latest state is a whole sync — every environment it reported —
// with each environment's issues counted by substatus.
func TestListLatestSentryEnvironmentsForProject_LatestSyncWithIssueCounts(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	f := seedProjectKindFixture(t, db, "acme-psnap-sentry")
	s := tenant.FromRun(db, f.orgID)

	b := f.bind(t, db, f.compA, "sentry", "web", false)
	retired := f.bind(t, db, f.compB, "sentry", "old", true)
	var prodID uuid.UUID
	withOrgTxCM(t, db, f.orgID, func(tx *sqlx.Tx) {
		ins := `INSERT INTO sentry_snapshots (org_id, binding_id, component_id, environment, project_slug, total_issues, collected_at, captured_at)
		        VALUES ($1, $2, $3, $4, 'web', $5, now() - $6::interval, now() - $6::interval) RETURNING id`
		var ignored uuid.UUID
		for _, row := range []struct {
			env, ago string
			total    int
			dst      *uuid.UUID
		}{
			{"staging", "2 days", 9, &ignored}, // an earlier sync
			{"production", "1 hour", 3, &prodID},
			{"staging", "1 hour", 1, &ignored},
		} {
			if err := tx.Get(row.dst, ins, f.orgID, b, f.compA, row.env, row.total, row.ago); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Get(&ignored, ins, f.orgID, retired, f.compB, "production", 7, "1 minute"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`
			INSERT INTO sentry_issues (org_id, snapshot_id, snapshot_captured_at, issue_id, substatus, event_count)
			SELECT $1, id, captured_at, v.i, v.sub, v.n FROM sentry_snapshots,
			       (VALUES ('1', 'new', 4), ('2', 'escalating', 10), ('3', 'new', 1)) v(i, sub, n)
			WHERE id = $2`, f.orgID, prodID); err != nil {
			t.Fatal(err)
		}
	})

	got, err := repo.ListLatestSentryEnvironmentsForProject(ctx, s, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Environment != "production" || got[1].Environment != "staging" {
		t.Fatalf("got %+v, want the latest sync's production and staging", got)
	}
	prod, staging := got[0], got[1]
	if prod.TotalIssues != 3 || prod.NewCount != 2 || prod.EscalatingCount != 1 || prod.RegressedCount != 0 || prod.Events != 15 {
		t.Fatalf("production = %+v", prod)
	}
	if staging.TotalIssues != 1 || staging.Events != 0 {
		t.Fatalf("staging = %+v, want the latest sync's 1 issue, no events", staging)
	}
}
