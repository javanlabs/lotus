package scheduler

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// enqueuerLockKey is the fixed advisory-lock key every lotus process
// contends for to become the enqueuer — an arbitrary but stable int8,
// chosen once and never reused for anything else in this schema.
const enqueuerLockKey = 8817_0301

// AcquireLeader tries to become the enqueuer leader via a session-level
// Postgres advisory lock (task 3.2: "leader-elected lewat Postgres
// advisory lock, jadi N replica aman"). pg_advisory_lock is tied to the
// physical connection that took it, not to a transaction, so this holds
// one dedicated *sql.Conn out of the pool for as long as leadership is
// held — release() must be called exactly once to both unlock and return
// that connection to the pool. acquired is false (release still safe to
// call, a no-op) when another process already holds the lock.
func AcquireLeader(ctx context.Context, db *sqlx.DB) (release func(), acquired bool, err error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return nil, false, err
	}
	var gotLock bool
	if err := conn.QueryRowxContext(ctx, `SELECT pg_try_advisory_lock($1)`, int64(enqueuerLockKey)).Scan(&gotLock); err != nil {
		conn.Close()
		return nil, false, err
	}
	if !gotLock {
		conn.Close()
		return func() {}, false, nil
	}
	release = func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(enqueuerLockKey))
		conn.Close()
	}
	return release, true, nil
}
