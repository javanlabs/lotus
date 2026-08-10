package collector

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/tenant"
)

// PersistFunc writes a successful Collect's kind-specific Detail into its
// own snapshot table (gitlab_snapshots, sonar_snapshots, ...) — task 3.6's
// "halaman detail cuma baca tabelnya sendiri" rule needs a real table to
// read, and task 3.3's own acceptance criteria requires a partial-failure
// Collect to still leave a saved snapshot behind. Registered alongside a
// Collector in the same init(), same reasoning as the Collector registry
// itself: the scheduler (task 3.2) calls this by Kind, never by importing
// gitlab_snapshots/sonar_snapshots/... directly — a fifth integration's
// snapshot table is exactly as invisible to internal/scheduler as its
// Collector implementation is.
type PersistFunc func(ctx context.Context, s tenant.Scope, componentID, bindingID uuid.UUID, r Result) error

var (
	persistMu  sync.RWMutex
	persisters = map[Kind]PersistFunc{}
)

func RegisterPersister(k Kind, fn PersistFunc) {
	persistMu.Lock()
	defer persistMu.Unlock()
	if _, exists := persisters[k]; exists {
		panic("collector: duplicate persister registration for kind " + string(k))
	}
	persisters[k] = fn
}

func GetPersister(k Kind) (PersistFunc, bool) {
	persistMu.RLock()
	defer persistMu.RUnlock()
	fn, ok := persisters[k]
	return fn, ok
}
