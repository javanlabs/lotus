package gitlab_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	glcollector "github.com/uyab/lotus/internal/collector/gitlab"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxGL(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String()); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func seedGLFixture(t *testing.T, db *sqlx.DB, subdomain string) (orgID, componentID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxGL(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'gitlab', 'gl', 'https://gitlab.example.com') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, 'gitlab', 'group/repo') RETURNING id`, orgID, componentID, connID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, componentID, bindingID
}

func TestPersist_PartialResultStillSavesSnapshot(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedGLFixture(t, db, "acme")

	srv := fakeGitLabServer(t, true) // languages fails
	defer srv.Close()
	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	scope := tenant.FromRun(db, orgID)
	if err := glcollector.Persist(context.Background(), scope, componentID, bindingID, result); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	snap, err := repo.LatestGitlabSnapshot(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("LatestGitlabSnapshot: %v", err)
	}
	if snap.PrimaryLanguage != nil {
		t.Fatalf("expected primary_language to be NULL (languages call failed), got %v", *snap.PrimaryLanguage)
	}
	if snap.BranchesTotal == nil {
		t.Fatal("expected branches_total to still be set (branches call succeeded)")
	}
}

func TestPersist_CrossOrgSnapshotsNeverMixDespiteIdenticalExternalKey(t *testing.T) {
	db := dbtest.Open(t)
	orgA, compA, bindA := seedGLFixture(t, db, "acme")
	orgB, compB, bindB := seedGLFixture(t, db, "beta") // same external_key "group/repo" in both orgs

	srv := fakeGitLabServer(t, false)
	defer srv.Close()
	c := glcollector.Collector{}

	sessionA, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token-a", Client: srv.Client()})
	resultA, err := sessionA.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if err != nil {
		t.Fatalf("collect A: %v", err)
	}
	sessionB, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token-b", Client: srv.Client()})
	resultB, err := sessionB.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if err != nil {
		t.Fatalf("collect B: %v", err)
	}

	scopeA := tenant.FromRun(db, orgA)
	scopeB := tenant.FromRun(db, orgB)
	if err := glcollector.Persist(context.Background(), scopeA, compA, bindA, resultA); err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if err := glcollector.Persist(context.Background(), scopeB, compB, bindB, resultB); err != nil {
		t.Fatalf("persist B: %v", err)
	}

	snapA, err := repo.LatestGitlabSnapshot(context.Background(), scopeA, bindA)
	if err != nil {
		t.Fatalf("LatestGitlabSnapshot A: %v", err)
	}
	snapB, err := repo.LatestGitlabSnapshot(context.Background(), scopeB, bindB)
	if err != nil {
		t.Fatalf("LatestGitlabSnapshot B: %v", err)
	}
	if snapA.ID == snapB.ID {
		t.Fatal("org A and org B must never share a snapshot row, even with an identical external_key")
	}
	if snapA.BindingID != bindA || snapB.BindingID != bindB {
		t.Fatal("snapshots must be bound to their own org's binding")
	}

	// org A cannot see org B's snapshot even by binding id (RLS).
	_, err = repo.LatestGitlabSnapshot(context.Background(), scopeA, bindB)
	if err == nil {
		t.Fatal("expected org A's scope to not see org B's snapshot")
	}
}

func TestRateLimiter_ThrottlesOutboundCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"username":"tester"}`)
	}))
	defer srv.Close()

	limiter := scheduler.NewLimiters(2, 1) // 2 req/sec, burst 1
	connID := uuid.New()
	client := &http.Client{Transport: scheduler.WrapTransport(http.DefaultTransport, limiter.For(connID))}

	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: client})
	defer session.Close()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := session.Ping(context.Background()); err != nil {
			t.Fatalf("Ping %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	// 3 calls at burst=1, 2/sec: first is immediate, the next two each
	// wait ~500ms — expect at least ~900ms total, well above what an
	// unthrottled loop of local HTTP calls would ever take.
	if elapsed < 900*time.Millisecond {
		t.Fatalf("elapsed = %v, want >= ~900ms (rate limiter should have throttled these calls)", elapsed)
	}
}
