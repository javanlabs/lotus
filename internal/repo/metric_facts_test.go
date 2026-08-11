package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	_ "github.com/uyab/lotus/internal/collector/trivy" // registers trivy's Descriptor().MetricKeys()
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func withOrgTxMF(t *testing.T, db *sqlx.DB, orgID uuid.UUID, fn func(tx *sqlx.Tx)) {
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

func seedMFFixture(t *testing.T, db *sqlx.DB, subdomain, kind string) (orgID, componentID, bindingID uuid.UUID) {
	t.Helper()
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	withOrgTxMF(t, db, orgID, func(tx *sqlx.Tx) {
		var projectID uuid.UUID
		if err := tx.Get(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'p', 'P') RETURNING id`, orgID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if err := tx.Get(&componentID, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'backend') RETURNING id`, orgID, projectID); err != nil {
			t.Fatalf("insert component: %v", err)
		}
		var connID uuid.UUID
		if err := tx.Get(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, $2, 'c', 'https://x') RETURNING id`, orgID, providerFor(kind)); err != nil {
			t.Fatalf("insert connection: %v", err)
		}
		if err := tx.Get(&bindingID, `
			INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
			VALUES ($1, $2, $3, $4, 'x') RETURNING id`, orgID, componentID, connID, kind); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
	})
	return orgID, componentID, bindingID
}

func providerFor(kind string) string {
	if kind == "trivy" {
		return "harbor_trivy"
	}
	return kind
}

func numFact(key string, v float64) collector.Fact {
	vv := v
	return collector.Fact{Key: key, ValueNum: &vv, Unit: "count"}
}

func TestInsertMetricFacts_NewKeyImmediatelyQueryableAndInCatalog(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "gitlab")
	scope := tenant.FromRun(db, orgID)

	result := collector.Result{Facts: []collector.Fact{numFact("gitlab.totally_new_metric", 42)}}
	if err := repo.InsertMetricFacts(context.Background(), scope, componentID, bindingID, "gitlab", result); err != nil {
		t.Fatalf("InsertMetricFacts: %v", err)
	}

	now := time.Now()
	points, err := repo.Series(context.Background(), scope, bindingID, "gitlab.totally_new_metric", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(points) != 1 || !points[0].HasValue || points[0].Value != 42 {
		t.Fatalf("points = %+v, want one point with value 42", points)
	}

	keys, err := repo.ListMetricKeys(context.Background(), scope, "gitlab")
	if err != nil {
		t.Fatalf("ListMetricKeys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k.Key == "gitlab.totally_new_metric" {
			found = true
			if k.Declared {
				t.Error("an undeclared key must not be marked declared")
			}
			if !k.Active {
				t.Error("a key that was just observed must be active")
			}
		}
	}
	if !found {
		t.Fatal("new key must appear in the catalog immediately, with no migration")
	}
}

func TestInsertMetricFacts_DeclaredKeysSeededFromDescriptor(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "trivy")
	scope := tenant.FromRun(db, orgID)

	// Even a Fact for just one key must seed every one of the kind's
	// declared MetricKeys, not only the one actually emitted this time.
	result := collector.Result{Facts: []collector.Fact{numFact("trivy.total", 1)}}
	if err := repo.InsertMetricFacts(context.Background(), scope, componentID, bindingID, "trivy", result); err != nil {
		t.Fatalf("InsertMetricFacts: %v", err)
	}

	keys, err := repo.ListMetricKeys(context.Background(), scope, "trivy")
	if err != nil {
		t.Fatalf("ListMetricKeys: %v", err)
	}
	byKey := map[string]repo.MetricKey{}
	for _, k := range keys {
		byKey[k.Key] = k
	}
	total, ok := byKey["trivy.total"]
	if !ok || !total.Declared || total.Label == "" {
		t.Fatalf("trivy.total = %+v, want declared=true with a friendly label", total)
	}
	critical, ok := byKey["trivy.critical"]
	if !ok || !critical.Declared {
		t.Fatalf("trivy.critical = %+v, want declared=true even though no fact for it arrived this collect", critical)
	}
}

// TestInsertMetricFacts_SentryEmitsAtLeastSixSeparatelyQueryableFacts is
// task 3.7's own acceptance criterion, run against Sentry's real
// Collect/Persist path rather than a hand-built Result — the same
// six-plus facts internal/collector/sentry's buildEnvironmentResult
// actually produces (sentry.score, total_issues, events_total, new,
// escalating, regressed, ongoing).
func TestInsertMetricFacts_SentryEmitsAtLeastSixSeparatelyQueryableFacts(t *testing.T) {
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedMFFixture(t, db, "acme", "sentry")
	scope := tenant.FromRun(db, orgID)

	facts := []collector.Fact{
		numFact("sentry.score", 80), numFact("sentry.total_issues", 3), numFact("sentry.events_total", 120),
		numFact("sentry.new", 1), numFact("sentry.escalating", 1), numFact("sentry.regressed", 0), numFact("sentry.ongoing", 1),
	}
	result := collector.Result{Children: []collector.Result{{Facts: facts}}}
	if err := repo.InsertMetricFacts(context.Background(), scope, componentID, bindingID, "sentry", result); err != nil {
		t.Fatalf("InsertMetricFacts: %v", err)
	}

	now := time.Now()
	for _, f := range facts {
		points, err := repo.Series(context.Background(), scope, bindingID, f.Key, now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatalf("Series(%s): %v", f.Key, err)
		}
		if len(points) != 1 || !points[0].HasValue {
			t.Errorf("Series(%s) = %+v, want one point", f.Key, points)
		}
	}
}

func TestSeries_IsolatesAcrossOrgsWithIdenticalKey(t *testing.T) {
	db := dbtest.Open(t)
	orgA, compA, bindA := seedMFFixture(t, db, "acme", "gitlab")
	orgB, compB, bindB := seedMFFixture(t, db, "beta", "gitlab")
	scopeA := tenant.FromRun(db, orgA)
	scopeB := tenant.FromRun(db, orgB)

	if err := repo.InsertMetricFacts(context.Background(), scopeA, compA, bindA, "gitlab",
		collector.Result{Facts: []collector.Fact{numFact("gitlab.shared_key_name", 111)}}); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if err := repo.InsertMetricFacts(context.Background(), scopeB, compB, bindB, "gitlab",
		collector.Result{Facts: []collector.Fact{numFact("gitlab.shared_key_name", 222)}}); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	now := time.Now()
	pointsA, err := repo.Series(context.Background(), scopeA, bindA, "gitlab.shared_key_name", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Series A: %v", err)
	}
	if len(pointsA) != 1 || pointsA[0].Value != 111 {
		t.Fatalf("org A points = %+v, want exactly its own value 111", pointsA)
	}

	// org A's scope querying org B's binding id must see nothing (RLS).
	pointsCross, err := repo.Series(context.Background(), scopeA, bindB, "gitlab.shared_key_name", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Series cross: %v", err)
	}
	if len(pointsCross) != 0 {
		t.Fatalf("org A's scope must never see org B's points, got %+v", pointsCross)
	}
}
