// Package crypto implements task 2.2's envelope encryption: a
// per-organization Data Encryption Key (DEK), wrapped by a single Key
// Encryption Key (KEK) sourced from LOTUS_KEK, with every credential
// sealed under its own org's DEK using the org's id as Additional
// Authenticated Data. AES-256-GCM everywhere — DEK and KEK are both
// 32-byte keys, nonces are the GCM-standard 12 bytes, generated fresh per
// seal.
//
// AAD is what makes a ciphertext non-portable between organizations: GCM
// authenticates (without encrypting) the AAD alongside the ciphertext, so
// decrypting row B's ciphertext while asserting org A's id as AAD fails
// authentication even with the exact right key — copying a credential
// row from one organization to another is not a plain UPDATE anymore, it
// is a cryptographic dead end.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

// DEKSize and nonce size are both fixed by AES-256-GCM's requirements.
const DEKSize = 32

// ErrDecryptFailed covers every GCM authentication failure — wrong key,
// wrong AAD (task 2.2's cross-org test), or corrupted ciphertext. The
// underlying crypto/cipher error is deliberately not wrapped through: it
// carries no information beyond "authentication failed" and Go's own GCM
// implementation guarantees it never leaks timing or content information,
// so there is nothing to lose by collapsing it to a single sentinel.
var ErrDecryptFailed = errors.New("crypto: decryption failed")

// NewDEK generates a fresh random 256-bit key.
func NewDEK() ([]byte, error) {
	dek := make([]byte, DEKSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	return dek, nil
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under key (a DEK or the KEK), authenticating aad
// alongside it. Returns ciphertext and the nonce Open needs to reverse it
// — both must be stored; neither is secret on its own; the nonce must
// never be reused with the same key (fresh random nonce per call, per
// Go's crypto/cipher.AEAD contract, is what guarantees that here).
func Seal(key, aad, plaintext []byte) (ciphertext, nonce []byte, err error) {
	aead, err := gcmFor(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, aad)
	return ciphertext, nonce, nil
}

// Open reverses Seal. Returns ErrDecryptFailed if key, aad, ciphertext, or
// nonce don't all match what Seal produced together.
func Open(key, aad, ciphertext, nonce []byte) ([]byte, error) {
	aead, err := gcmFor(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return pt, nil
}

// OrgAAD is the Additional Authenticated Data every credential in task
// 2.2 is sealed with: the owning organization's id, as its canonical
// string form. Binding to the id (not, say, the row's own primary key)
// is what specifically prevents inter-organization copying — every
// credential row an organization owns shares the same AAD, but no other
// organization's rows ever do.
func OrgAAD(orgID uuid.UUID) []byte {
	return []byte(orgID.String())
}

// Fingerprint returns a short, non-reversible identifier for plaintext —
// task 2.2's secret_fingerprint, used to detect "did the credential
// actually change" without ever decrypting the old value to compare. It
// is intentionally truncated (8 bytes of a SHA-256 digest, hex-encoded):
// long enough that two different real-world tokens practically never
// collide, short enough to signal at a glance "this is a fingerprint, not
// the secret" to anyone who sees it in a log or a UI.
func Fingerprint(plaintext []byte) string {
	sum := sha256.Sum256(plaintext)
	return hex.EncodeToString(sum[:8])
}
