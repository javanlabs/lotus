package collector

import (
	"errors"
	"time"
)

// ErrorClass is task 3.1's error taxonomy — the scheduler (task 3.2)
// switches on this, not on the underlying error, to decide whether to
// retry, how long to back off, and what side effect to apply to the
// connection or binding.
type ErrorClass string

const (
	// ErrClassAuth (401/403): never retried. Freezes every binding on the
	// connection and marks it auth_failed — an expired token shouldn't
	// silently keep failing 200 bindings one at a time.
	ErrClassAuth ErrorClass = "auth"
	// ErrClassNotFound (404): never retried. After 3 consecutive
	// occurrences the binding is marked broken; its last snapshot stays
	// visible but stale.
	ErrClassNotFound ErrorClass = "not_found"
	// ErrClassRateLimited (429): retried after RetryAfter. Sets the
	// connection's rate_limited_until so every worker — not just this
	// one — skips it until the window clears.
	ErrClassRateLimited ErrorClass = "rate_limited"
	// ErrClassUpstream covers 5xx and other transient upstream failures:
	// exponential backoff, up to 5 attempts, then a circuit breaker.
	ErrClassUpstream ErrorClass = "upstream"
	// ErrClassTimeout is the same retry/backoff treatment as Upstream,
	// kept distinct because its cause (network, not the upstream service
	// itself) is worth telling apart when reading a run's error_class.
	ErrClassTimeout ErrorClass = "timeout"
	// ErrClassConfig: never retried, not the collector's fault — a
	// malformed binding config that should have been caught by
	// ValidateConfig (task 2.3) but wasn't. Surfaced on the binding.
	ErrClassConfig ErrorClass = "config"
	// ErrClassInternal: never retried, is the collector's fault (a bug).
	// Surfaced on the binding and alerted, not shown to the tenant as
	// something they can fix.
	ErrClassInternal ErrorClass = "internal"
)

// CollectError wraps an error with the class the scheduler needs to act
// on it. Every collector's Session methods should return one of these
// (via the ErrX constructors below) for any failure that isn't a plain Go
// error from something structurally wrong in the caller's own request.
type CollectError struct {
	Class ErrorClass
	Err   error
	// RetryAfter is meaningful only for ErrClassRateLimited — the
	// upstream's own Retry-After, parsed by the caller.
	RetryAfter time.Duration
}

func (e *CollectError) Error() string { return e.Err.Error() }
func (e *CollectError) Unwrap() error { return e.Err }

func ErrAuth(err error) *CollectError     { return &CollectError{Class: ErrClassAuth, Err: err} }
func ErrNotFound(err error) *CollectError { return &CollectError{Class: ErrClassNotFound, Err: err} }
func ErrUpstream(err error) *CollectError { return &CollectError{Class: ErrClassUpstream, Err: err} }
func ErrTimeout(err error) *CollectError  { return &CollectError{Class: ErrClassTimeout, Err: err} }
func ErrConfig(err error) *CollectError   { return &CollectError{Class: ErrClassConfig, Err: err} }
func ErrInternal(err error) *CollectError { return &CollectError{Class: ErrClassInternal, Err: err} }

func ErrRateLimited(err error, retryAfter time.Duration) *CollectError {
	return &CollectError{Class: ErrClassRateLimited, Err: err, RetryAfter: retryAfter}
}

// ClassOf extracts the ErrorClass from err, defaulting to ErrClassInternal
// for any error a collector returned that isn't a *CollectError — an
// un-classified error is exactly as dangerous as a miscategorized one
// (the scheduler would otherwise retry something it shouldn't, or vice
// versa), so it's treated as the least-retryable, most-visible class
// rather than silently defaulting to a retry loop.
func ClassOf(err error) ErrorClass {
	var ce *CollectError
	if errors.As(err, &ce) {
		return ce.Class
	}
	return ErrClassInternal
}

// RetryAfterOf returns the CollectError's RetryAfter, or zero if err
// isn't a rate-limited CollectError.
func RetryAfterOf(err error) time.Duration {
	var ce *CollectError
	if errors.As(err, &ce) && ce.Class == ErrClassRateLimited {
		return ce.RetryAfter
	}
	return 0
}
