package db_test

import (
	"context"
	"testing"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func TestSweepExpiredSessions(t *testing.T) {
	sqlDB := dbtest.Open(t)
	ctx := context.Background()

	var userID string
	if err := sqlDB.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('a@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := sqlDB.Exec(`
		INSERT INTO sessions (id, user_id, host, expires_at) VALUES
		('expired-1', $1, 'auth.test', now() - interval '1 hour'),
		('expired-2', $1, 'auth.test', now() - interval '1 minute'),
		('live-1', $1, 'auth.test', now() + interval '1 hour')`, userID); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	n, err := global.SweepExpiredSessions(ctx, sqlDB)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("swept %d rows, want 2", n)
	}

	var remaining int
	if err := sqlDB.Get(&remaining, `SELECT count(*) FROM sessions`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining sessions = %d, want 1", remaining)
	}
}
