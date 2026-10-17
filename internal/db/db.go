// Package db opens the Postgres connection pool and runs goose migrations.
//
// Migrations are embedded in the binary but only ever run via `lotus
// migrate` (see cmd/lotus), never at process boot — with multiple replicas,
// running DDL at boot means processes race on schema changes, and the app
// role (lotus_app) intentionally has no DDL rights so RLS containment (task
// 0.3) can't be bypassed by a bug in application code.
package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to Postgres via the pure-Go pgx driver under sqlx, so the
// `db.Select`/`db.Get` + `db:"col"` struct-tag style carries over unchanged
// wherever it's ported from the old Lotus codebase. Used only for
// migrations (task 0.4's lotus_migrate role needs its real DDL rights, not
// lotus_app's restricted ones) — application code should use OpenApp.
func Open(dsn string) (*sqlx.DB, error) {
	return sqlx.Open("pgx", dsn)
}

// OpenApp connects for application/runtime use and forces every physical
// connection onto the lotus_app role via SET ROLE before it's ever handed
// out of the pool.
//
// Why this exists: FORCE ROW LEVEL SECURITY (migration 00003) does not
// apply to a session's role if that role is a superuser — RLS is bypassed
// unconditionally for superusers, no matter how the policy is written.
// DATABASE_URL in local dev (and TEST_DATABASE_URL in CI) both connect as
// the bootstrap `postgres` superuser for convenience (README: "one role is
// fine" locally), which means tenant.Scope's SET LOCAL app.org_id was
// having zero effect on query results in every environment except a
// production deploy that happens to hand the app a real non-superuser
// lotus_app credential — RLS, the fail-safe net the architecture notes
// describe, was silently a no-op everywhere else. This surfaced only when
// task 2.1 added the first tenant-plane query path that actually depends
// on RLS to reject a cross-org id (see the TENSION task logged alongside
// this fix).
//
// SET ROLE lotus_app succeeds whether the connecting role is the `postgres`
// superuser (can SET ROLE to anything) or is already `lotus_app` itself (a
// role can always switch to itself) — so this is safe to use unconditionally
// in every environment rather than branching dev/test vs. production
// behavior. If DATABASE_URL is ever pointed at a role that is neither a
// superuser nor a member of lotus_app, connections fail closed instead of
// silently running with elevated, RLS-bypassing privileges.
func OpenApp(dsn string) (*sqlx.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	sqlDB := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE lotus_app")
		return err
	}))
	return sqlx.NewDb(sqlDB, "pgx"), nil
}

// Migrate applies all pending migrations. Called only from `lotus migrate
// up`, using the lotus_migrate role's DSN.
func Migrate(dsn string) error {
	sqlDB, err := Open(dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(sqlDB.DB, "migrations")
}

// MigrateDown rolls back the most recent migration. Exposed for local dev
// and tests; not wired to a production CLI flag in Fase 0.
func MigrateDown(dsn string) error {
	sqlDB, err := Open(dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Down(sqlDB.DB, "migrations")
}

// MigrateDownTo rolls back every migration after version. A test of one
// migration goes back to just before it this way, however many have
// landed since.
func MigrateDownTo(dsn string, version int64) error {
	sqlDB, err := Open(dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.DownTo(sqlDB.DB, "migrations", version)
}

// MigrationsChecksum fingerprints the embedded migration set.
//
// It exists for the test harness (internal/dbtest), which builds one
// migrated template database and clones it per test instead of running
// every migration per test. That template has to be invalidated when the
// migrations change, and a content hash does it automatically — the
// alternative, a hand-bumped version constant, is the kind of thing that
// gets forgotten exactly once and then costs an afternoon to a test suite
// running against a stale schema.
func MigrationsChecksum() string {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		// The FS is embedded at build time; a failure here is not a
		// runtime condition a caller could do anything about.
		panic("db: reading embedded migrations: " + err.Error())
	}
	// fs.ReadDir returns entries sorted by filename, so the hash is stable
	// across machines and runs.
	h := sha256.New()
	for _, e := range entries {
		b, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			panic("db: reading embedded migration " + e.Name() + ": " + err.Error())
		}
		h.Write([]byte(e.Name()))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
