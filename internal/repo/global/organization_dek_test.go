package global_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func testKEK(t *testing.T, seed byte, version int) *crypto.KEK {
	t.Helper()
	raw := make([]byte, crypto.DEKSize)
	for i := range raw {
		raw[i] = seed
	}
	kek, err := crypto.LoadKEK(base64.StdEncoding.EncodeToString(raw), version)
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	return kek
}

func TestGetOrCreateOrgDEK_LazyAndStable(t *testing.T) {
	db := dbtest.Open(t)
	kek := testKEK(t, 0x01, 1)

	var orgID string
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme', 'Acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}

	dek1, err := global.GetOrCreateOrgDEK(context.Background(), db, kek, mustParseUUID(t, orgID))
	if err != nil {
		t.Fatalf("first GetOrCreateOrgDEK: %v", err)
	}
	dek2, err := global.GetOrCreateOrgDEK(context.Background(), db, kek, mustParseUUID(t, orgID))
	if err != nil {
		t.Fatalf("second GetOrCreateOrgDEK: %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Fatal("GetOrCreateOrgDEK must return the same DEK on repeated calls, not generate a new one each time")
	}
}

func TestRewrapOrgDEKs_SameDEKUnderNewKEK(t *testing.T) {
	db := dbtest.Open(t)
	oldKEK := testKEK(t, 0x01, 1)
	newKEK := testKEK(t, 0x02, 2)

	var orgID string
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme', 'Acme') RETURNING id`); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	id := mustParseUUID(t, orgID)

	original, err := global.GetOrCreateOrgDEK(context.Background(), db, oldKEK, id)
	if err != nil {
		t.Fatalf("GetOrCreateOrgDEK: %v", err)
	}

	n, err := global.RewrapOrgDEKs(context.Background(), db, oldKEK, newKEK)
	if err != nil {
		t.Fatalf("RewrapOrgDEKs: %v", err)
	}
	if n != 1 {
		t.Fatalf("rewrapped %d organizations, want 1", n)
	}

	// The DEK itself is unchanged — only which KEK wraps it — so anything
	// encrypted under the old DEK (every existing credential) is still
	// readable without touching a single credential row.
	afterRewrap, err := global.GetOrCreateOrgDEK(context.Background(), db, newKEK, id)
	if err != nil {
		t.Fatalf("GetOrCreateOrgDEK after rewrap: %v", err)
	}
	if string(original) != string(afterRewrap) {
		t.Fatal("re-wrapping must not change the underlying DEK")
	}

	// The old KEK can no longer unwrap it (proves the rewrap actually
	// happened, not a no-op).
	if _, err := global.GetOrCreateOrgDEK(context.Background(), db, oldKEK, id); err == nil {
		t.Fatal("expected the old KEK to no longer be able to unwrap the DEK after rewrap")
	}
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return parsed
}
