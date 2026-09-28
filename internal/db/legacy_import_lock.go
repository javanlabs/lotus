package db

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// The advisory lock `lotus import-legacy` holds for its whole run, as the
// two-key form (LegacyImportLockClass, LegacyImportLockID). While it is
// held, the retention and timeseries passes leave partitions alone: the
// import writes history into past months that those passes would
// otherwise drop mid-import, before it is rolled up.
const (
	LegacyImportLockClass = 4271
	LegacyImportLockID    = 1
)

// LegacyImportRunning reports whether an import holds the lock, on any
// connection. A failed check counts as running: skipping one maintenance
// pass costs nothing, dropping history an import is writing cannot be
// undone.
func LegacyImportRunning(ctx context.Context, q sqlx.QueryerContext) bool {
	var held bool
	err := sqlx.GetContext(ctx, q, &held, `
		SELECT EXISTS (SELECT 1 FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 2 AND granted)`,
		LegacyImportLockClass, LegacyImportLockID)
	return err != nil || held
}
