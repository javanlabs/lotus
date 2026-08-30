// Package dbtest gives Go tests an isolated, migrated Postgres database.
//
// Each test gets its own database, cloned from a template that was
// migrated once. The obvious design — a fresh schema per test, migrated
// from zero — is what this replaced: internal/web alone built 64 schemas
// and spent 28.7s of its 53s local runtime inside migration 00018, which
// creates 60 partitions plus their policies and grants. On CI's slower
// runner that package took 247s and, since packages run in parallel, set
// the floor for the whole job at over five minutes. Cloning is a file
// copy in Postgres, so the per-test cost stops scaling with the number of
// migrations.
//
// The isolation guarantee is unchanged and is the point: one database per
// test, so RLS and cross-org containment are exercised for real rather
// than approximated by tests politely not looking at each other's rows.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/db"
)

var dbSeq atomic.Int64

// maxIdentifier is Postgres's NAMEDATALEN-1. Postgres truncates anything
// longer without complaining, which would silently merge two long test
// names into one database.
const maxIdentifier = 63

// databaseName derives a database name unique to one test in one process.
//
// The pid is load-bearing. dbSeq is per-process, so two packages that
// happen to contain a test of the same name would otherwise both produce
// "t_<thatname>_1" — and `go test ./...` runs packages in parallel, so the
// two binaries raced on the same target. internal/retention and
// internal/timeseries both define TestEnsurePartitions_CreatesFutureMonths,
// so this was not hypothetical: it failed in CI while passing locally
// under `go test -p 1`, which serializes packages and hides it.
func databaseName(testName string) string {
	suffix := fmt.Sprintf("_%d_%d", os.Getpid(), dbSeq.Add(1))
	base := "t_" + sanitize(strings.ToLower(testName))
	if len(base)+len(suffix) > maxIdentifier {
		base = base[:maxIdentifier-len(suffix)]
	}
	return base + suffix
}

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// templateLockKey serializes template construction between concurrent test
// binaries. Any constant works; this one is just unlikely to collide with
// an advisory lock taken by application code.
const templateLockKey = 0x107_5CEC

// ensureTemplate builds (or finds) the migrated template database and
// returns its name. Safe to call from many processes at once: the work is
// done under a Postgres advisory lock, so the first binary to arrive
// migrates and the rest wait and reuse.
//
// The name carries a checksum of the migration set, so editing any
// migration produces a different template rather than silently reusing a
// stale schema. Old templates are left behind deliberately — dropping them
// would race with a concurrently running older binary, and they cost a few
// megabytes on a test server.
func ensureTemplate(dsn string) (string, error) {
	templateOnce.Do(func() {
		name := "lotus_tmpl_" + db.MigrationsChecksum()

		maint, err := maintenanceDSN(dsn)
		if err != nil {
			templateErr = err
			return
		}
		admin, err := sql.Open("pgx", maint)
		if err != nil {
			templateErr = fmt.Errorf("connect to maintenance database: %w", err)
			return
		}
		defer admin.Close()

		// One connection for the whole critical section: an advisory lock
		// is held by the session that took it, and database/sql would
		// otherwise hand the unlock to a different pooled connection.
		conn, err := admin.Conn(context.Background())
		if err != nil {
			templateErr = fmt.Errorf("maintenance connection: %w", err)
			return
		}
		defer conn.Close()

		if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
			templateErr = fmt.Errorf("advisory lock: %w", err)
			return
		}
		defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", templateLockKey)

		var exists bool
		if err := conn.QueryRowContext(context.Background(),
			"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
			templateErr = fmt.Errorf("look up template: %w", err)
			return
		}
		if exists {
			templateName = name
			return
		}

		// Build under a scratch name and rename on success, so a crash
		// mid-migration cannot leave a half-migrated database sitting
		// there under the name everything else is about to clone.
		scratch := fmt.Sprintf("%s_building_%d", name, os.Getpid())
		if _, err := conn.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+scratch+`" WITH (FORCE)`); err != nil {
			templateErr = fmt.Errorf("drop stale scratch template: %w", err)
			return
		}
		if _, err := conn.ExecContext(context.Background(), `CREATE DATABASE "`+scratch+`"`); err != nil {
			templateErr = fmt.Errorf("create scratch template: %w", err)
			return
		}
		scratchDSN, err := withDatabase(dsn, scratch)
		if err != nil {
			templateErr = err
			return
		}
		if err := db.Migrate(scratchDSN); err != nil {
			conn.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+scratch+`" WITH (FORCE)`)
			templateErr = fmt.Errorf("migrate template: %w", err)
			return
		}
		// db.Migrate closes its pool, but a connection can linger briefly;
		// RENAME refuses while anyone is attached.
		if _, err := conn.ExecContext(context.Background(), `ALTER DATABASE "`+scratch+`" RENAME TO "`+name+`"`); err != nil {
			// Another binary won the race between our EXISTS check and
			// here; its template is equally valid, so use it.
			var nowExists bool
			if e := conn.QueryRowContext(context.Background(),
				"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&nowExists); e == nil && nowExists {
				conn.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+scratch+`" WITH (FORCE)`)
				templateName = name
				return
			}
			templateErr = fmt.Errorf("promote template: %w", err)
			return
		}
		templateName = name
	})
	return templateName, templateErr
}

// DSN clones the migrated template into a database of this test's own and
// returns a DSN pointing at it. Skips the test if TEST_DATABASE_URL is
// unset (run via `make test`, which sets it). Exposed separately from Open
// so tests that need to re-run migrations themselves (an idempotency
// check) can do so against the same isolated database.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test`")
	}

	tmpl, err := ensureTemplate(dsn)
	if err != nil {
		t.Fatalf("prepare template database: %v", err)
	}

	name := databaseName(t.Name())
	maint, err := maintenanceDSN(dsn)
	if err != nil {
		t.Fatalf("%v", err)
	}
	admin, err := sql.Open("pgx", maint)
	if err != nil {
		t.Fatalf("connect to maintenance database: %v", err)
	}
	defer admin.Close()

	// CREATE DATABASE ... TEMPLATE fails if anything is connected to the
	// template, and two clones starting at the same instant can see each
	// other. Retrying beats holding a global lock: the copy takes tens of
	// milliseconds, and serializing every test in every package behind one
	// lock would give back much of the speed this change is for.
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		_, lastErr = admin.Exec(`CREATE DATABASE "` + name + `" TEMPLATE "` + tmpl + `"`)
		if lastErr == nil {
			break
		}
		if !strings.Contains(lastErr.Error(), "being accessed by other users") {
			break
		}
		time.Sleep(time.Duration(25*(attempt+1)) * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("clone template into %s: %v", name, lastErr)
	}

	t.Cleanup(func() {
		a, err := sql.Open("pgx", maint)
		if err != nil {
			return
		}
		defer a.Close()
		// FORCE: the test's own pool may not have finished closing, and a
		// leaked test database is worse than a terminated idle connection.
		a.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
	})

	scoped, err := withDatabase(dsn, name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return scoped
}

// Open clones the template and returns a *sqlx.DB connected to the result.
//
// Uses db.OpenApp, not db.Open: TEST_DATABASE_URL connects as a superuser,
// which bypasses Row-Level Security unconditionally (FORCE ROW LEVEL
// SECURITY has no effect on a superuser session) — a plain Open here would
// make every tenant-isolation test pass for the wrong reason, having
// verified nothing about RLS at all. OpenApp's SET ROLE lotus_app makes
// tests see exactly what production's RLS-restricted connection sees.
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

// maintenanceDSN points at the server's default database. CREATE DATABASE
// and DROP DATABASE cannot run from a connection to the database being
// created or dropped.
func maintenanceDSN(dsn string) (string, error) {
	return withDatabase(dsn, "postgres")
}

func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse TEST_DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// sanitize keeps only characters legal in an unquoted Postgres identifier,
// so subtest names (which contain "/") produce a valid database name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}
