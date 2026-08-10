package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// newPKCE returns a random code verifier and its S256 challenge (RFC 7636).
func newPKCE() (verifier, challenge string) {
	verifier = randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// randToken returns a random URL-safe token with n bytes of entropy.
func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the process can't do anything safely
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// sha256Hex hashes s and returns it hex-encoded — used for every
// single-use-token-at-rest column in this package (oauth state, handoff
// code/verifier, session id).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
