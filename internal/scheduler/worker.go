package scheduler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// ExecuteFunc runs one claimed SyncRun and returns its Result — the
// caller (cmd/lotus, once task 3.3+ collectors exist) resolves the
// binding/connection, decrypts the credential, opens a Collector Session
// (rate-limited via WrapTransport), and calls Collect. Kept as an
// injected function rather than a fixed dependency so this package's own
// tests can supply a mock collector without needing a real one to exist
// yet — task 3.2 is explicitly not responsible for any collector's
// content.
type ExecuteFunc func(ctx context.Context, run *global.SyncRun) (collector.Result, error)

// brokenAfterConsecutiveNotFound is task 3.1's "Setelah 3 kali berturut-
// turut → binding broken" rule.
const brokenAfterConsecutiveNotFound = 3

// maxUpstreamRetries is task 3.1's "Circuit breaker setelah 5 kegagalan
// beruntun" for upstream/timeout errors.
const maxUpstreamRetries = 5

type Pool struct {
	DB            *sqlx.DB
	WorkerID      string
	LeaseDuration time.Duration
	Execute       ExecuteFunc
}

// ClaimAndRun performs one claim → mark-started → execute → finish cycle.
// Returns claimed=false when nothing was claimable right now (not an
// error — an idle pool is the normal state).
func (p *Pool) ClaimAndRun(ctx context.Context) (claimed bool, err error) {
	atCapacity, err := global.OrgsAtCapacity(ctx, p.DB)
	if err != nil {
		return false, err
	}
	run, err := global.ClaimRun(ctx, p.DB, p.WorkerID, atCapacity, p.LeaseDuration)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}

	if err := global.MarkRunStarted(ctx, p.DB, run.ID); err != nil {
		return true, err
	}

	result, execErr := p.Execute(ctx, run)
	return true, p.handleResult(ctx, run, result, execErr)
}

func (p *Pool) handleResult(ctx context.Context, run *global.SyncRun, result collector.Result, execErr error) error {
	scope := tenant.FromRun(p.DB, run.OrgID)

	if execErr != nil {
		return p.handleError(ctx, scope, run, execErr)
	}

	if err := collector.ValidateResult(result); err != nil {
		// A collector bug (empty Facts on success), not a run to retry —
		// task 3.1's own acceptance criterion. Surfaced as internal, not
		// silently swallowed.
		msg := err.Error()
		class := string(collector.ErrClassInternal)
		return global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "failed", ErrorClass: &class, Error: &msg})
	}

	if err := global.RecordConnectionSuccess(ctx, p.DB, run.ConnectionID); err != nil {
		return err
	}

	// Persist the kind's own detail snapshot — task 3.3's own acceptance
	// criteria requires a partial-failure Collect to still leave a saved
	// snapshot behind. component_metrics_current (task 3.6) is upserted
	// from inside persist itself. A kind with no registered persister
	// (true for any kind that hasn't shipped a snapshot table yet) simply
	// skips this step rather than failing the run over it.
	binding, err := repo.BindingByID(ctx, scope, run.BindingID)
	if err != nil {
		return err
	}
	if persist, ok := collector.GetPersister(collector.Kind(run.Kind)); ok {
		if err := persist(ctx, scope, binding.ComponentID, run.BindingID, result); err != nil {
			return err
		}
	}

	// metric_facts (task 3.7) — generic, kind-agnostic: every Fact any
	// collector emitted (including on Children, for a kind like Sentry
	// that fans out per environment) is written the same way regardless
	// of kind, same as the dashboard projection above.
	if err := repo.InsertMetricFacts(ctx, scope, binding.ComponentID, run.BindingID, run.Kind, result); err != nil {
		return err
	}

	status := "ok"
	var warningsJSON []byte
	if len(result.Warnings) > 0 {
		// Partial failure is a first-class result (task 3.1): the run
		// still succeeded, and whatever was collected is still stored.
		status = "partial"
		warningsJSON, _ = json.Marshal(result.Warnings)
	}
	if err := global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: status, WarningsJSON: warningsJSON}); err != nil {
		return err
	}

	nextSyncAt := time.Now().Add(EnqueueJitter(DefaultSyncInterval))
	return repo.UpdateBindingAfterRun(ctx, scope, run.BindingID, nextSyncAt, status, 0)
}

func (p *Pool) handleError(ctx context.Context, scope tenant.Scope, run *global.SyncRun, execErr error) error {
	class := collector.ClassOf(execErr)
	classStr := string(class)
	msg := execErr.Error()

	switch class {
	case collector.ErrClassAuth:
		// Freezes every binding on the connection at once, not just this
		// one — task 3.1's rule, the whole reason auth gets its own class.
		if err := repo.FreezeBindingsForConnection(ctx, scope, run.ConnectionID); err != nil {
			return err
		}
		if err := repo.SetConnectionTestResult(ctx, scope, run.ConnectionID, "auth_failed", msg); err != nil {
			return err
		}
		return global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "failed", ErrorClass: &classStr, Error: &msg})

	case collector.ErrClassNotFound:
		binding, err := repo.BindingByID(ctx, scope, run.BindingID)
		if err != nil {
			return err
		}
		failures := binding.ConsecutiveFailures + 1
		lastStatus := "fail"
		if failures >= brokenAfterConsecutiveNotFound {
			lastStatus = "broken"
		}
		if err := repo.UpdateBindingAfterRun(ctx, scope, run.BindingID, time.Now().Add(EnqueueJitter(DefaultSyncInterval)), lastStatus, failures); err != nil {
			return err
		}
		return global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "failed", ErrorClass: &classStr, Error: &msg})

	case collector.ErrClassRateLimited:
		until := time.Now().Add(collector.RetryAfterOf(execErr))
		if err := global.SetConnectionRateLimited(ctx, p.DB, run.ConnectionID, until); err != nil {
			return err
		}
		if err := global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "skipped", ErrorClass: &classStr, Error: &msg}); err != nil {
			return err
		}
		// Re-enqueue for after the window clears — every worker already
		// skips a rate-limited connection's runs via ClaimRun, so this
		// doesn't risk hammering it again the instant it reopens.
		_, _, err := global.EnqueueRun(ctx, p.DB, global.NewSyncRun{
			OrgID: run.OrgID, ConnectionID: run.ConnectionID, BindingID: run.BindingID, Kind: run.Kind,
			Trigger: "scheduled", Priority: run.Priority, ScheduledFor: until,
		})
		return err

	case collector.ErrClassUpstream, collector.ErrClassTimeout:
		failures, err := global.SetConnectionBackoff(ctx, p.DB, run.ConnectionID, time.Now().Add(BackoffDuration(run.Attempt)))
		if err != nil {
			return err
		}
		if err := global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "failed", ErrorClass: &classStr, Error: &msg}); err != nil {
			return err
		}
		if failures >= maxUpstreamRetries {
			return nil // circuit breaker: stop retrying until something else (a config change) clears backoff
		}
		_, _, err = global.EnqueueRun(ctx, p.DB, global.NewSyncRun{
			OrgID: run.OrgID, ConnectionID: run.ConnectionID, BindingID: run.BindingID, Kind: run.Kind,
			Trigger: "scheduled", Priority: run.Priority, ScheduledFor: time.Now().Add(BackoffDuration(run.Attempt)),
		})
		return err

	default: // config, internal — never retried, not the tenant's problem to see as "flaky"
		return global.FinishRun(ctx, p.DB, run.ID, global.FinishRunParams{Status: "failed", ErrorClass: &classStr, Error: &msg})
	}
}
