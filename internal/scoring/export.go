package scoring

import "time"

// EvalSentryStatus is internal/collector/sentry's entry point into the
// ported score in sentry.go. Kept in a separate file, as a thin wrapper
// around the unexported evalSentryStatus, so sentry.go itself stays a
// faithful port of the legacy sentry_score.go — task 3.5's own
// instruction was "don't rewrite it", and every symbol it ported stayed
// unexported exactly as it was there.
func EvalSentryStatus(mapped, hasSnapshot bool, envCount int, signals []SentryIssueSignal, now time.Time) SentryStatus {
	return evalSentryStatus(mapped, hasSnapshot, envCount, signals, now)
}
