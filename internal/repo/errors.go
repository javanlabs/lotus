package repo

import (
	"database/sql"
	"errors"
)

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// checkRowsAffected turns a zero-row UPDATE/DELETE into ErrNotFound. RLS
// makes a cross-org id simply match no row rather than error, so this is
// what actually produces the 404-not-403 behavior for mutations (reads get
// it for free from sql.ErrNoRows on a scoped SELECT).
func checkRowsAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
