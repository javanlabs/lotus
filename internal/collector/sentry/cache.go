package sentry

import (
	"context"
	"sync"
	"time"

	sentryclient "github.com/uyab/lotus/internal/clients/sentry"
)

// projectCacheTTL bounds how long a cached Project() fetch is trusted —
// task 3.5's "map ber-TTL" (a TTL map), not an unbounded one, so a
// project rename/re-team eventually shows up without a process restart.
const projectCacheTTL = 5 * time.Minute

type projectCacheEntry struct {
	project   sentryclient.Project
	err       error
	fetchedAt time.Time
}

// projectCache memoizes Project() fetches so N bindings sharing one
// Sentry project cost one fetch, not N — task 3.5's single most important
// structural carry-over from the legacy sync job's own sentryProjectCtx.
//
// It is a package-level singleton, not a per-Session field: the real
// scheduler (internal/scheduler.DefaultExecute) opens a fresh Session for
// every single claimed run, not once per batch, so a Session-scoped cache
// would be created and destroyed on every binding and never actually
// share anything. A process-lifetime, TTL-bounded, package-level cache
// achieves the ticket's actual goal — bound real Sentry API traffic —
// regardless of Session lifetime, and does it slightly better: it also
// covers bindings collected in different scheduler batches, not just one.
// Every Session still reaches it (via get, below), so from a call-site
// view the cache reads as "the Session's project cache" even though its
// backing store outlives any one Session.
type projectCache struct {
	mu      sync.Mutex
	entries map[string]*projectCacheEntry
}

var sharedProjectCache = &projectCache{entries: map[string]*projectCacheEntry{}}

// key incorporates the secret, not just baseURL+org+slug: two connections
// that happen to point at the same Sentry org+project must never share a
// cache entry, or one connection's auth failure could poison another
// connection's otherwise-valid access to what looks like "the same"
// project.
func cacheKey(baseURL, org, secret, slug string) string {
	return baseURL + "\x00" + org + "\x00" + secret + "\x00" + slug
}

// get returns slug's Project, fetching (and caching, success or failure)
// on a miss. The lock is held across the network call rather than
// released and reacquired: Sentry syncs are not latency-sensitive, and
// holding it is what actually guarantees "one fetch, not N" under
// concurrent callers for the same key — releasing it around the fetch
// would let two workers claiming two bindings on the same project both
// miss the cache and both fetch, exactly the traffic multiplication this
// cache exists to prevent.
func (c *projectCache) get(ctx context.Context, client *sentryclient.Client, baseURL, org, secret, slug string) (sentryclient.Project, error) {
	key := cacheKey(baseURL, org, secret, slug)
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok && time.Since(e.fetchedAt) < projectCacheTTL {
		return e.project, e.err
	}
	project, err := client.Project(ctx, slug)
	c.entries[key] = &projectCacheEntry{project: project, err: err, fetchedAt: time.Now()}
	return project, err
}
