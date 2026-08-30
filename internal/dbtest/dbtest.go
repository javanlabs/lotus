// Package dbtest gives Go tests an isolated, migrated Postgres schema.
//
// Every test gets its own schema (via the connection's search_path) on the
// shared TEST_DATABASE_URL database, so tests can run without a container
// per test and without clobbering each other's rows. Mirrors the pattern
// used by talisa's internal/store test harness.
package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/db"
)

var schemaSeq atomic.Int64

// maxIdentifier is Postgres's NAMEDATALEN-1. Postgres truncates anything
// longer without complaining, which would silently merge two long test
// names into one schema.
const maxIdentifier = 63

// schemaName derives a schema unique to one test in one process.
//
// The pid is load-bearing. schemaSeq is per-process, so two packages that
// happen to contain a test of the same name each produced
// "t_<thatname>_1" — and `go test ./...` runs packages in parallel, so
// both binaries raced on the same schema: each dropped it out from under
// the other mid-migration, failing with "no schema has been selected to
// create in". internal/retention and internal/timeseries both define
// TestEnsurePartitions_CreatesFutureMonths, so this was not hypothetical;
// it failed in CI while passing locally under `go test -p 1`, which
// serializes packages and hides it.
func schemaName(testName string) string {
	suffix := fmt.Sprintf("_%d_%d", os.Getpid(), schemaSeq.Add(1))
	base := "t_" + sanitize(strings.ToLower(testName))
	if len(base)+len(suffix) > maxIdentifier {
		base = base[:maxIdentifier-len(suffix)]
	}
	return base + suffix
}

// DSN creates a fresh schema, migrates it with goose, and returns a DSN
// scoped to that schema (via search_path). Skips the test if
// TEST_DATABASE_URL is unset (run via `make test`, which sets it). Exposed
// separately from Open so tests that need to re-run migrations themselves
// (e.g. an idempotency check) can do so against the same isolated schema.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test`")
	}

	name := schemaName(t.Name())

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin conn: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS "` + name + `" CASCADE`); err != nil {
		t.Fatalf("drop schema %s: %v", name, err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA "` + name + `"`); err != nil {
		t.Fatalf("create schema %s: %v", name, err)
	}
	t.Cleanup(func() {
		a, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer a.Close()
		a.Exec(`DROP SCHEMA IF EXISTS "` + name + `" CASCADE`)
	})

	scoped := withSearchPath(dsn, name)
	if err := db.Migrate(scoped); err != nil {
		t.Fatalf("migrate schema %s: %v", name, err)
	}
	return scoped
}

// Open creates a fresh schema, migrates it with goose, and returns a
// *sqlx.DB whose connections default to that schema.
//
// Uses db.OpenApp, not db.Open: TEST_DATABASE_URL connects as the
// `postgres` superuser, which bypasses Row-Level Security unconditionally
// (FORCE ROW LEVEL SECURITY has no effect on a superuser session) — a
// plain Open here would make every tenant-isolation test pass for the
// wrong reason, having verified nothing about RLS at all. OpenApp's SET
// ROLE lotus_app makes tests see exactly what production's RLS-restricted
// connection sees.
func Open(t *testing.T) *sqlx.DB {
	t.Helper()
	scoped := DSN(t)
	sqlxDB, err := db.OpenApp(scoped)
	if err != nil {
		t.Fatalf("open sqlx conn: %v", err)
	}
	t.Cleanup(func() { sqlxDB.Close() })
	return sqlxDB
}

func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// sanitize keeps only characters legal in an unquoted Postgres identifier,
// so subtest names (which contain "/") produce a valid schema name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}
