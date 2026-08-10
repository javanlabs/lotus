// Package scheduler implements task 3.2: the enqueuer that finds due
// bindings, the claim-and-execute worker pool, and the three fairness
// mechanisms (per-org concurrency quota, deficit ordering, per-connection
// rate limiting) the spec calls out as the difference between "works in
// dev" and "the first paying customer's IP gets rate-limited by GitLab and
// every organization goes stale at once."
package scheduler

import (
	"net/http"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Limiters holds one rate.Limiter per connection_id — never per host.
// Task 3.2's own reasoning: two organizations both on gitlab.com must not
// share a bucket (one org's abuse would throttle the other's completely
// unrelated traffic), but every binding *within* one org's own GitLab
// connection must share theirs (40 bindings hitting the same PAT are one
// client as far as GitLab's own rate limit is concerned).
type Limiters struct {
	mu sync.Mutex
	m  map[uuid.UUID]*rate.Limiter
	// limit/burst are applied to every connection uniformly for now — a
	// per-connection override (from Descriptor.CostHint, task 3.1) is a
	// natural extension once a real collector needs a tighter budget than
	// this default.
	limit rate.Limit
	burst int
}

// NewLimiters builds a registry defaulting to ratePerSecond requests/sec
// per connection with the given burst.
func NewLimiters(ratePerSecond float64, burst int) *Limiters {
	return &Limiters{m: map[uuid.UUID]*rate.Limiter{}, limit: rate.Limit(ratePerSecond), burst: burst}
}

// For returns (creating if needed) the limiter for connID.
func (l *Limiters) For(connID uuid.UUID) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.m[connID]
	if !ok {
		lim = rate.NewLimiter(l.limit, l.burst)
		l.m[connID] = lim
	}
	return lim
}

// rateLimitedTransport wraps an http.RoundTripper with a per-connection
// limiter, injected into the http.Client a Collector.Open builds its
// Session's requests through — this is the mechanism, not a policy
// decision the collector itself has to know about.
type rateLimitedTransport struct {
	next    http.RoundTripper
	limiter *rate.Limiter
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

// WrapTransport returns a RoundTripper that rate-limits through limiter
// before delegating to next (http.DefaultTransport if next is nil).
func WrapTransport(next http.RoundTripper, limiter *rate.Limiter) http.RoundTripper {
	return &rateLimitedTransport{next: next, limiter: limiter}
}
