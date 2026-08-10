package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
)

func TestScopeTxSetsAppOrgID(t *testing.T) {
	db := dbtest.Open(t)
	orgID := uuid.New()
	s := newScope(db, orgID)

	var seen string
	err := s.Tx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Get(&seen, `SELECT current_setting('app.org_id', true)`)
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if seen != orgID.String() {
		t.Fatalf("app.org_id inside Tx = %q, want %q", seen, orgID.String())
	}
}

func TestScopeTxDoesNotLeakAcrossScopes(t *testing.T) {
	db := dbtest.Open(t)
	org1, org2 := uuid.New(), uuid.New()
	s1, s2 := newScope(db, org1), newScope(db, org2)

	var seen1, seen2 string
	if err := s1.Tx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Get(&seen1, `SELECT current_setting('app.org_id', true)`)
	}); err != nil {
		t.Fatalf("s1.Tx: %v", err)
	}
	if err := s2.Tx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Get(&seen2, `SELECT current_setting('app.org_id', true)`)
	}); err != nil {
		t.Fatalf("s2.Tx: %v", err)
	}
	if seen1 != org1.String() || seen2 != org2.String() {
		t.Fatalf("seen1=%q seen2=%q, want %q and %q", seen1, seen2, org1, org2)
	}

	// SET LOCAL is transaction-scoped, not session-scoped: once both
	// transactions have committed, a plain query on the same pool must not
	// still see either org's setting. If Scope.Tx used SET instead of
	// set_config(..., true) (SET LOCAL), this would leak org2's id forever
	// on whichever pooled connection last ran it.
	var after string
	if err := db.Get(&after, `SELECT current_setting('app.org_id', true)`); err != nil {
		t.Fatalf("post-Tx query: %v", err)
	}
	if after != "" {
		t.Fatalf("app.org_id leaked outside its transaction: %q", after)
	}
}
