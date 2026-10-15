package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// dbtest.Open connects as lotus_app, the non-superuser role the running
// app uses, so Row-Level Security is enforced here exactly as it is in
// production. The importer's earlier shape was only ever run as a
// superuser, which RLS never binds; under this connection every one of
// its inserts would have failed the tenant policy.

var testRegistry = legacyRegistry{Services: []legacyService{
	{Name: "authentication-service", Kind: "backend", GitlabPat: "alurkerja/be/auth", SonarKey: "ak-auth"},
	{Name: "app-react-v2", Kind: "frontend", GitlabPat: "alurkerja/fe/app", SonarKey: "ak-app"},
	{Name: "syarq-api", Product: "syarq", Kind: "backend", GitlabPat: "syarq/api"},
}}

func seedImportOrg(t *testing.T) (*sqlx.DB, uuid.UUID) {
	t.Helper()
	conn := dbtest.Open(t)
	var orgID uuid.UUID
	if err := conn.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	return conn, orgID
}

func scopedCount(t *testing.T, conn *sqlx.DB, orgID uuid.UUID, query string) int {
	t.Helper()
	var n int
	err := tenant.FromRun(conn, orgID).Tx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Get(&n, query)
	})
	if err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestImportServices_OnlyTheRequestedProductUnderRLS(t *testing.T) {
	conn, orgID := seedImportOrg(t)

	sum, err := importServices(context.Background(), conn, orgID, testRegistry, map[string]bool{"alurkerja": true})
	if err != nil {
		t.Fatalf("import as the restricted app role: %v", err)
	}
	if len(sum.Projects) != 1 || sum.Projects[0].Product != "alurkerja" || sum.Components != 2 {
		t.Fatalf("summary = %+v, want one alurkerja project with 2 components", sum)
	}
	if sum.BindingsCreated != 0 || len(sum.Connected) != 0 {
		t.Fatalf("with no connections, want no bindings; got %+v", sum)
	}

	if n := scopedCount(t, conn, orgID, `SELECT count(*) FROM projects`); n != 1 {
		t.Errorf("projects = %d, want 1 — syarq must not be imported", n)
	}
	if n := scopedCount(t, conn, orgID, `SELECT count(*) FROM components`); n != 2 {
		t.Errorf("components = %d, want 2", n)
	}
}

// Connections come later, through the sealed flow. Running the import
// again then fills in exactly the bindings that were skipped — and running
// it a third time changes nothing.
func TestImportServices_RerunAfterAddingAConnectionFillsInBindingsOnce(t *testing.T) {
	conn, orgID := seedImportOrg(t)
	ctx := context.Background()
	only := map[string]bool{"alurkerja": true}

	if _, err := importServices(ctx, conn, orgID, testRegistry, only); err != nil {
		t.Fatalf("first import: %v", err)
	}

	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatalf("dek: %v", err)
	}
	if _, err := repo.CreateConnection(ctx, tenant.FromRun(conn, orgID), dek, repo.NewConnection{
		Provider: "gitlab", Name: "GitLab", BaseURL: "https://gitlab.example.test",
		Config: json.RawMessage(`{}`), Secret: "dummy-test-token",
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	second, err := importServices(ctx, conn, orgID, testRegistry, only)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !second.Connected["gitlab"] || second.BindingsCreated != 2 {
		t.Fatalf("second import = %+v, want 2 gitlab bindings created", second)
	}

	third, err := importServices(ctx, conn, orgID, testRegistry, only)
	if err != nil {
		t.Fatalf("third import: %v", err)
	}
	if third.BindingsCreated != 0 || third.BindingsExisting != 2 {
		t.Fatalf("third import = %+v, want nothing new and 2 already present", third)
	}
	if n := scopedCount(t, conn, orgID, `SELECT count(*) FROM components`); n != 2 {
		t.Errorf("components = %d after three imports, want 2 — re-running must not duplicate", n)
	}
	if n := scopedCount(t, conn, orgID, `SELECT count(*) FROM metric_bindings`); n != 2 {
		t.Errorf("bindings = %d, want 2", n)
	}
}

// sentry: false keeps an AlurKerja service out of the shared Sentry
// project, even with a Sentry connection there to bind to.
func TestImportServices_SentryFalseSkipsTheSharedSentryBinding(t *testing.T) {
	conn, orgID := seedImportOrg(t)
	ctx := context.Background()

	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatalf("dek: %v", err)
	}
	if _, err := repo.CreateConnection(ctx, tenant.FromRun(conn, orgID), dek, repo.NewConnection{
		Provider: "sentry", Name: "Sentry", BaseURL: "https://sentry.example.test",
		Config: json.RawMessage(`{"organization":"javan"}`), Secret: "dummy-test-token",
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	off := false
	reg := legacyRegistry{Services: []legacyService{
		{Name: "authentication-service", Kind: "backend"},
		{Name: "camunda-bpm-platform", Label: "Camunda", Kind: "backend", Sentry: &off},
	}}
	sum, err := importServices(ctx, conn, orgID, reg, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if sum.BindingsCreated != 1 {
		t.Fatalf("summary = %+v, want the one sentry binding for authentication-service", sum)
	}
	if n := scopedCount(t, conn, orgID, `SELECT count(*) FROM metric_bindings b JOIN components c ON c.id = b.component_id
		WHERE c.slug = 'camunda-bpm-platform'`); n != 0 {
		t.Errorf("camunda-bpm-platform has %d bindings, want none", n)
	}
}
