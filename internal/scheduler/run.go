package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// DefaultExecute builds the ExecuteFunc cmd/lotus wires into a real Pool:
// resolve the binding/connection, decrypt the credential, open a
// rate-limited, SSRF-guarded Session against whatever Collector is
// registered for the run's Kind, and Collect. A kind with no registered
// Collector (true for every kind until tasks 3.3-3.5 land) fails with
// ErrConfig rather than panicking or hanging — the queue and worker pool
// are already fully functional before any real collector exists; this is
// what makes that true end to end instead of only at the unit level.
func DefaultExecute(db *sqlx.DB, kek *crypto.KEK, netguardCfg netguard.Config, limiters *Limiters) ExecuteFunc {
	return func(ctx context.Context, run *global.SyncRun) (collector.Result, error) {
		c, ok := collector.Get(collector.Kind(run.Kind))
		if !ok {
			return collector.Result{}, collector.ErrConfig(fmt.Errorf("no collector registered for kind %q", run.Kind))
		}

		scope := tenant.FromRun(db, run.OrgID)
		conn, err := repo.ConnectionByID(ctx, scope, run.ConnectionID)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}
		binding, err := repo.BindingByID(ctx, scope, run.BindingID)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}
		// organizations.settings — task 3.4's trivy_fail_on is the first
		// reader; without this, CollectRequest.OrgSettings was always nil
		// on the real scheduler path, silently defeating that policy.
		orgSettings, err := global.OrgSettings(ctx, db, run.OrgID)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}

		dek, err := global.GetOrCreateOrgDEK(ctx, db, kek, run.OrgID)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}
		secret, err := repo.DecryptConnectionSecret(ctx, scope, dek, run.ConnectionID)
		if err != nil {
			return collector.Result{}, collector.ErrInternal(err)
		}

		client := netguard.NewClient(netguardCfg)
		client.Transport = WrapTransport(client.Transport, limiters.For(run.ConnectionID))

		session, err := c.Open(ctx, collector.ConnConfig{BaseURL: conn.BaseURL, Secret: secret, Client: client, Config: conn.Config, Provider: conn.Provider})
		if err != nil {
			return collector.Result{}, err
		}
		defer session.Close()

		return session.Collect(ctx, collector.CollectRequest{
			OrgID: run.OrgID, ComponentID: binding.ComponentID, BindingID: binding.ID,
			ExternalKey: binding.ExternalKey, Config: binding.Config, OrgSettings: orgSettings,
		})
	}
}

// Options configures Run's background loops.
type Options struct {
	DB            *sqlx.DB
	Execute       ExecuteFunc
	WorkerCount   int
	LeaseDuration time.Duration
	EnqueueEvery  time.Duration
	ReapEvery     time.Duration
	PollEvery     time.Duration // how often an idle worker checks for new work
	// RepoScans turns on repository scans: the enqueuer gives components
	// with GitLab and no container image a Trivy binding that scans the
	// repository. Off where the host cannot run Trivy.
	RepoScans bool
}

func (o *Options) setDefaults() {
	if o.WorkerCount <= 0 {
		o.WorkerCount = 4
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 10 * time.Minute
	}
	if o.EnqueueEvery <= 0 {
		o.EnqueueEvery = 30 * time.Second
	}
	if o.ReapEvery <= 0 {
		o.ReapEvery = time.Minute
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 2 * time.Second
	}
}

// Run starts the enqueuer (leader-elected), the reaper, and WorkerCount
// worker goroutines, blocking until ctx is cancelled. Safe to run in every
// replica: only one process's enqueuer ever actually does anything at a
// time (AcquireLeader), and the worker pool's claim query (SKIP LOCKED)
// is already safe under any number of concurrent workers, in this process
// or another one entirely.
func Run(ctx context.Context, opts Options) {
	opts.setDefaults()

	go runEnqueuer(ctx, opts.DB, opts.EnqueueEvery, opts.RepoScans)
	go runReaper(ctx, opts.DB, opts.ReapEvery)

	for i := 0; i < opts.WorkerCount; i++ {
		go runWorker(ctx, &Pool{DB: opts.DB, WorkerID: fmt.Sprintf("worker-%d", i), LeaseDuration: opts.LeaseDuration, Execute: opts.Execute}, opts.PollEvery)
	}

	<-ctx.Done()
}

func runEnqueuer(ctx context.Context, db *sqlx.DB, every time.Duration, repoScans bool) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			release, acquired, err := AcquireLeader(ctx, db)
			if err != nil {
				log.Printf("scheduler: enqueuer leader acquisition: %v", err)
				continue
			}
			if !acquired {
				continue
			}
			if repoScans {
				if n, err := EnsureRepoScanBindingsAllOrgs(ctx, db); err != nil {
					log.Printf("scheduler: repository scan bindings: %v", err)
				} else if n > 0 {
					log.Printf("scheduler: added %d repository scan binding(s)", n)
				}
			}
			n, err := EnqueueDueBindingsAllOrgs(ctx, db)
			release()
			if err != nil {
				log.Printf("scheduler: enqueue due bindings: %v", err)
			} else if n > 0 {
				log.Printf("scheduler: enqueued %d run(s)", n)
			}
		}
	}
}

func runReaper(ctx context.Context, db *sqlx.DB, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := global.ReapExpiredLeases(ctx, db)
			if err != nil {
				log.Printf("scheduler: reap expired leases: %v", err)
			} else if n > 0 {
				log.Printf("scheduler: reaped %d expired lease(s)", n)
			}
		}
	}
}

func runWorker(ctx context.Context, pool *Pool, pollEvery time.Duration) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			claimed, err := pool.ClaimAndRun(ctx)
			if err != nil {
				log.Printf("scheduler: worker %s: %v", pool.WorkerID, err)
				continue
			}
			if claimed {
				// Immediately check for more work rather than waiting out
				// the full poll interval — keeps a busy queue draining
				// quickly without needing a separate "keep going" signal.
				for {
					more, err := pool.ClaimAndRun(ctx)
					if err != nil {
						log.Printf("scheduler: worker %s: %v", pool.WorkerID, err)
						break
					}
					if !more {
						break
					}
				}
			}
		}
	}
}
