package scheduler

import (
	"math/rand"
	"time"
)

// BackoffDuration is task 3.2's formula: next_sync_at = now() + min(1h, 1m
// × 2^attempt), ± 20% jitter — so a burst of connections that all start
// failing at once (an upstream outage) don't all retry in lockstep and
// re-hammer it the moment it recovers.
func BackoffDuration(attempt int) time.Duration {
	base := time.Minute
	for i := 0; i < attempt && base < time.Hour; i++ {
		base *= 2
	}
	if base > time.Hour {
		base = time.Hour
	}
	return jitter(base, 0.20)
}

// EnqueueJitter spreads a batch of simultaneously-created bindings (e.g.
// 500 from one onboarding wizard) across a ±10% window around their
// nominal scheduled time, so they don't all fire in the same instant
// forever after.
func EnqueueJitter(d time.Duration) time.Duration {
	return jitter(d, 0.10)
}

func jitter(d time.Duration, frac float64) time.Duration {
	delta := (rand.Float64()*2 - 1) * frac // uniform in [-frac, +frac]
	return d + time.Duration(float64(d)*delta)
}
