package tenant

import (
	"testing"
	"time"
	"unsafe"

	"github.com/uyab/lotus/internal/repo/global"
)

// Fiber's c.Hostname() is zero-copy: its bytes live in a request buffer
// that the next request overwrites. The cache kept that string as its key
// without copying it, so the "acme" key turned into "beta" under it; a
// lookup for "beta" then found acme's entry whenever the two hashes shared
// a slot — about one request in 150, seen as a random 401 in
// TestProjects_SlugUniquePerOrgNotGlobal. The cache must own its keys.
// Each fresh cache gets a new hash seed, so enough of them hit the
// collision for certain.
func TestOrgCache_KeysDoNotAliasTheRequestBuffer(t *testing.T) {
	acme := &global.Org{Subdomain: "acme"}
	for i := 0; i < 5000; i++ {
		c := newOrgCache(time.Minute)
		buf := []byte("acme") // stands in for fasthttp's request buffer
		c.set(unsafe.String(&buf[0], len(buf)), acme)
		copy(buf, "beta") // the next request reuses the buffer
		if org, ok := c.get("beta"); ok {
			t.Fatalf("attempt %d: beta resolved to %s's organization", i, org.Subdomain)
		}
		if _, ok := c.get("acme"); !ok {
			t.Fatalf("attempt %d: acme is no longer cached", i)
		}
	}
}
