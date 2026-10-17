package scheduler

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// DefaultSyncInterval is how long a binding waits between runs when
// neither it (sync_interval_seconds) nor its organization (Settings →
// Organization's default sync interval) says otherwise. An hour: every
// integration Lotus reads changes on the scale of hours — a SonarQube
// analysis, a Harbor scan, a day's commits — and polling four of them
// per component every 15 minutes mostly re-read unchanged data.
const DefaultSyncInterval = time.Hour

// EnqueueImmediate enqueues a manual-trigger run right away — task 3.2's
// deliberate behavior change from every prior scheduler: a binding gets
// data within seconds of being created, not after waiting out its first
// full interval. Called from the binding-creation and discovery-claim
// handlers (tasks 2.3/2.4); a no-op (via EnqueueRun's own dedupe) if
// something else already queued this binding.
func EnqueueImmediate(ctx context.Context, db *sqlx.DB, orgID, connID, bindingID uuid.UUID, kind string) error {
	_, _, err := global.EnqueueRun(ctx, db, global.NewSyncRun{
		OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: kind,
		Trigger: "manual", Priority: 10, ScheduledFor: time.Now(),
	})
	return err
}

// EnqueueDueBindingsForOrg scans one organization's bindings (via
// tenant.FromRun — see that function's doc comment for why this can't be
// a single cross-tenant query) and enqueues a scheduled run for every one
// whose next_sync_at has arrived. Returns how many were actually
// inserted (excludes any that deduped against an already-in-flight run).
func EnqueueDueBindingsForOrg(ctx context.Context, db *sqlx.DB, orgID uuid.UUID) (int, error) {
	scope := tenant.FromRun(db, orgID)
	due, err := repo.ListDueBindings(ctx, scope, time.Now())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range due {
		_, inserted, err := global.EnqueueRun(ctx, db, global.NewSyncRun{
			OrgID: orgID, ConnectionID: b.ConnectionID, BindingID: b.ID, Kind: b.Kind,
			Trigger: "scheduled", Priority: 100, ScheduledFor: time.Now(),
		})
		if err != nil {
			return n, err
		}
		if inserted {
			n++
		}
	}
	return n, nil
}

// EnqueueDueBindingsAllOrgs is the enqueuer's main tick: iterate every
// organization (control-plane, unscoped) and enqueue that org's due
// bindings. Only the elected leader (see AcquireLeader) should call this.
func EnqueueDueBindingsAllOrgs(ctx context.Context, db *sqlx.DB) (int, error) {
	var orgIDs []uuid.UUID
	if err := db.SelectContext(ctx, &orgIDs, `SELECT id FROM organizations WHERE deleted_at IS NULL`); err != nil {
		return 0, err
	}
	total := 0
	for _, orgID := range orgIDs {
		n, err := EnqueueDueBindingsForOrg(ctx, db, orgID)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// EnsureRepoScanBindingsAllOrgs gives every organization's components
// with a GitLab repository and no container image a repository-scan
// Trivy binding (repo.EnsureRepoScanBindings), so a GitLab binding made
// any way — the wizard, discovery, the seeder — is scanned without
// anyone asking. The enqueuer runs it before each tick when repository
// scans are on; the next due-bindings pass then picks the new ones up.
func EnsureRepoScanBindingsAllOrgs(ctx context.Context, db *sqlx.DB) (int, error) {
	var orgIDs []uuid.UUID
	if err := db.SelectContext(ctx, &orgIDs, `SELECT id FROM organizations WHERE deleted_at IS NULL`); err != nil {
		return 0, err
	}
	total := 0
	for _, orgID := range orgIDs {
		n, err := repo.EnsureRepoScanBindings(ctx, tenant.FromRun(db, orgID))
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
