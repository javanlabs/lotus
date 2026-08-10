package crypto_test

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/crypto"
)

func TestSealOpen_RoundTrip(t *testing.T) {
	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatalf("NewDEK: %v", err)
	}
	orgID := uuid.New()
	plaintext := []byte("glpat-supersecrettoken")

	ciphertext, nonce, err := crypto.Seal(dek, crypto.OrgAAD(orgID), plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := crypto.Open(dek, crypto.OrgAAD(orgID), ciphertext, nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

// TestSealOpen_WrongOrgAADFails is the crypto-layer proof behind task
// 2.2's cross-org acceptance criterion: a ciphertext copied to a row
// claiming a different org_id must fail to decrypt even under the exact
// same DEK, because AAD is bound into GCM's authentication tag.
func TestSealOpen_WrongOrgAADFails(t *testing.T) {
	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatalf("NewDEK: %v", err)
	}
	orgA, orgB := uuid.New(), uuid.New()
	ciphertext, nonce, err := crypto.Seal(dek, crypto.OrgAAD(orgA), []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := crypto.Open(dek, crypto.OrgAAD(orgB), ciphertext, nonce); err != crypto.ErrDecryptFailed {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}
}

func TestSealOpen_WrongKeyFails(t *testing.T) {
	dek1, _ := crypto.NewDEK()
	dek2, _ := crypto.NewDEK()
	orgID := uuid.New()
	ciphertext, nonce, err := crypto.Seal(dek1, crypto.OrgAAD(orgID), []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := crypto.Open(dek2, crypto.OrgAAD(orgID), ciphertext, nonce); err != crypto.ErrDecryptFailed {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}
}

func TestFingerprint_StableAndDistinguishing(t *testing.T) {
	a := crypto.Fingerprint([]byte("token-one"))
	b := crypto.Fingerprint([]byte("token-one"))
	c := crypto.Fingerprint([]byte("token-two"))
	if a != b {
		t.Fatal("fingerprint must be stable for the same input")
	}
	if a == c {
		t.Fatal("fingerprint must distinguish different inputs")
	}
}

func TestKEK_WrapUnwrapDEKRoundTrip(t *testing.T) {
	raw := make([]byte, crypto.DEKSize)
	for i := range raw {
		raw[i] = byte(i)
	}
	kek, err := crypto.LoadKEK(base64.StdEncoding.EncodeToString(raw), 1)
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}

	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatalf("NewDEK: %v", err)
	}
	ciphertext, nonce, err := kek.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	got, err := kek.UnwrapDEK(ciphertext, nonce)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatal("unwrapped DEK does not match original")
	}
}

func TestLoadKEK_RejectsBadInput(t *testing.T) {
	if _, err := crypto.LoadKEK("", 1); err != crypto.ErrKEKNotConfigured {
		t.Fatalf("empty key: err = %v, want ErrKEKNotConfigured", err)
	}
	if _, err := crypto.LoadKEK("not-valid-base64!!!", 1); err == nil {
		t.Fatal("expected an error for invalid base64")
	}
	shortKey := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := crypto.LoadKEK(shortKey, 1); err == nil {
		t.Fatal("expected an error for a key that isn't 32 bytes")
	}
}
