package db_test

import (
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
)

func TestCompositeFKRejectsOrgDrift(t *testing.T) {
	sqlDB := dbtest.Open(t)

	orgA := mustInsertOrg(t, sqlDB, "org-a")
	orgB := mustInsertOrg(t, sqlDB, "org-b")

	projectID := mustInsertProject(t, sqlDB, orgA, "proj")

	// A component naming project_id from org A but claiming org_id B must be
	// rejected by the composite FK, not silently accepted. app.org_id is set
	// to orgB (matching the row's own org_id) so RLS's WITH CHECK is
	// satisfied and the composite FK is the only thing left that can reject
	// this insert — otherwise a bare mismatch would fail for the wrong
	// reason (RLS, not the FK this test exists to prove).
	err := execAsOrg(t, sqlDB, orgB,
		`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'service')`,
		orgB, projectID)
	if err == nil {
		t.Fatal("expected org_id drift between component and its project to be rejected")
	}

	// The matching org_id succeeds, proving the rejection above was about
	// the mismatch specifically, not a broken schema.
	err = execAsOrg(t, sqlDB, orgA,
		`INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, 'c', 'C', 'service')`,
		orgA, projectID)
	if err != nil {
		t.Fatalf("expected matching org_id to succeed: %v", err)
	}
}

func TestRLSHidesOtherOrgRows(t *testing.T) {
	sqlDB := dbtest.Open(t)

	orgA := mustInsertOrg(t, sqlDB, "org-a")
	orgB := mustInsertOrg(t, sqlDB, "org-b")
	mustInsertProject(t, sqlDB, orgA, "shared-slug")
	mustInsertProject(t, sqlDB, orgB, "shared-slug")

	rows := queryAsTenantApp(t, sqlDB, orgA, `SELECT org_id FROM projects`)
	if len(rows) != 1 || rows[0] != orgA {
		t.Fatalf("scoped to org A, got rows %v, want exactly [%s]", rows, orgA)
	}
}

func TestMissingScopeYieldsZeroRows(t *testing.T) {
	sqlDB := dbtest.Open(t)

	orgA := mustInsertOrg(t, sqlDB, "org-a")
	mustInsertProject(t, sqlDB, orgA, "proj")

	// dbtest.Open already connects as lotus_app (see its doc comment); this
	// transaction deliberately never calls set_config('app.org_id', ...).
	tx, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.Get(&count, `SELECT count(*) FROM projects`); err != nil {
		t.Fatalf("select: %v", err)
	}
	if count != 0 {
		t.Fatalf("unscoped query returned %d rows, want 0", count)
	}
}

func TestLotusAppCannotDDL(t *testing.T) {
	sqlDB := dbtest.Open(t)

	tx, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE evil (id int)`); err == nil {
		t.Fatal("expected lotus_app to be rejected for CREATE TABLE")
	}
	// The failed statement above aborted the transaction; start a fresh one
	// for the ALTER TABLE check.
	tx.Rollback()

	tx2, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx2.Rollback()
	if _, err := tx2.Exec(`ALTER TABLE projects ADD COLUMN evil text`); err == nil {
		t.Fatal("expected lotus_app to be rejected for ALTER TABLE")
	}
}

// execAsOrg runs query in its own transaction with app.org_id set to orgID
// (mirroring tenant.Scope.Tx), committing on success. dbtest.Open's *sqlx.DB
// already connects as lotus_app, so this is exactly what a real request
// handler sees — every tenant-plane table is FORCE RLS (task 0.3), and a
// bare Exec/Get with no org_id set fails its WITH CHECK on INSERT or simply
// matches zero rows on SELECT.
func execAsOrg(t *testing.T, sqlDB *sqlx.DB, orgID, query string, args ...any) error {
	t.Helper()
	tx, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func mustInsertProject(t *testing.T, sqlDB *sqlx.DB, orgID, slug string) string {
	t.Helper()
	tx, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	var id string
	if err := tx.Get(&id, `INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, orgID, slug); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

// queryAsTenantApp runs query as lotus_app with app.org_id set to orgID,
// inside its own rolled-back transaction, and returns the org_id column of
// every row (query must SELECT org_id as its only/first column).
func queryAsTenantApp(t *testing.T, sqlDB *sqlx.DB, orgID, query string) []string {
	t.Helper()
	if !strings.Contains(strings.ToLower(query), "org_id") {
		t.Fatalf("query must select org_id: %s", query)
	}
	tx, err := sqlDB.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID); err != nil {
		t.Fatalf("set app.org_id: %v", err)
	}
	var rows []string
	if err := tx.Select(&rows, query); err != nil {
		t.Fatalf("query: %v", err)
	}
	return rows
}
