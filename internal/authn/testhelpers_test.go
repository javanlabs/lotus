package authn_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/uyab/lotus/internal/httpx"
)

// testRenderer points at the real repo templates so handoff.go's
// friendlyMessage/renderLoginPicker actually render, not just compile.
func testRenderer(t *testing.T) *httpx.Renderer {
	t.Helper()
	return httpx.NewRenderer(os.DirFS("../../web/templates"), true)
}

// sha256HexForTest mirrors the package's own (unexported) sha256Hex, for
// tests that need to seed a session/ticket row directly via SQL rather
// than through the handlers.
func sha256HexForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
