package crypto

import (
	"encoding/base64"
	"errors"
	"fmt"
)

// ErrKEKNotConfigured is returned by anything that needs the KEK when
// LOTUS_KEK is unset — credential storage (task 2.2) simply can't function
// without it, and every call site surfaces this rather than silently
// operating on a zero key.
var ErrKEKNotConfigured = errors.New("crypto: LOTUS_KEK is not configured")

// KEK is the single active Key Encryption Key, sourced from LOTUS_KEK
// (base64-encoded, 32 raw bytes — an AES-256 key). Versioned (Version,
// starting at 1) so a wrapped DEK can record which KEK last wrapped it;
// cmd/lotus rewrap-keys is what actually moves DEKs from an old KEK to a
// new one, LOTUS_KEK itself only ever holds the current one.
type KEK struct {
	Key     []byte
	Version int
}

// LoadKEK parses LOTUS_KEK's value. version is the version number to
// stamp on anything wrapped with this key — cmd/lotus increments it by
// reading the org's currently-stored dek_kek_version and expecting the
// caller (rewrap-keys) to pass the next one; ordinary app startup always
// passes 1, since a single long-lived KEK never needs its own version to
// change except during an explicit rotation.
func LoadKEK(base64Key string, version int) (*KEK, error) {
	if base64Key == "" {
		return nil, ErrKEKNotConfigured
	}
	raw, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("crypto: LOTUS_KEK is not valid base64: %w", err)
	}
	if len(raw) != DEKSize {
		return nil, fmt.Errorf("crypto: LOTUS_KEK must decode to %d bytes, got %d", DEKSize, len(raw))
	}
	return &KEK{Key: raw, Version: version}, nil
}

// WrapDEK encrypts a DEK under the KEK. No AAD here — a wrapped DEK isn't
// tied to a single organization by anything other than which row of
// organizations.dek_ciphertext it's stored in, so there is nothing
// meaningful to authenticate beyond the ciphertext and key themselves.
func (k *KEK) WrapDEK(dek []byte) (ciphertext, nonce []byte, err error) {
	return Seal(k.Key, nil, dek)
}

// UnwrapDEK reverses WrapDEK.
func (k *KEK) UnwrapDEK(ciphertext, nonce []byte) ([]byte, error) {
	return Open(k.Key, nil, ciphertext, nonce)
}
