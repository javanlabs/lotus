// Package db opens the Postgres connection pool and runs goose migrations.
//
// Migrations are embedded in the binary but only ever run via `lotus
// migrate` (see cmd/lotus), never at process boot — with multiple replicas,
// running DDL at boot means processes race on schema changes, and the app
// role (lotus_app) intentionally has no DDL rights so RLS containment (task
// 0.3) can't be bypassed by a bug in application code.
package db

import (
	"embed"

	"github.com/jmoiron/sqlx"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to Postgres via the pure-Go pgx driver under sqlx, so the
// `db.Select`/`db.Get` + `db:"col"` struct-tag style carries over unchanged
// wherever it's ported from the old Lotus codebase.
func Open(dsn string) (*sqlx.DB, error) {
	return sqlx.Open("pgx", dsn)
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
