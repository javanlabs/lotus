package db_test

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"

	lotusdb "github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
)

// The retention and timeseries passes warn when their role cannot read
// through row-level security; this is the check they use.
func TestSeesAllTenants(t *testing.T) {
	ctx := context.Background()
	db, err := sqlx.Open("pgx", dbtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if ok, err := lotusdb.SeesAllTenants(ctx, conn); err != nil || !ok {
		t.Fatalf("superuser: %v, %v; want true", ok, err)
	}
	// lotus_app is what the application runs as: RLS-bound.
	if _, err := conn.ExecContext(ctx, `SET ROLE lotus_app`); err != nil {
		t.Fatal(err)
	}
	if ok, err := lotusdb.SeesAllTenants(ctx, conn); err != nil || ok {
		t.Fatalf("lotus_app: %v, %v; want false", ok, err)
	}
}
