package collector

import "sync"

var (
	registryMu sync.RWMutex
	registry   = map[Kind]Collector{}
)

// Register adds c to the registry under k — called from each concrete
// collector package's init(), so wiring a new integration into the
// scheduler is one import (cmd/lotus) and nothing else. Panics on a
// duplicate registration: two packages claiming the same Kind is a build-
// time programming error, not a runtime condition to handle gracefully.
func Register(k Kind, c Collector) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[k]; exists {
		panic("collector: duplicate registration for kind " + string(k))
	}
	registry[k] = c
}

func Get(k Kind) (Collector, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[k]
	return c, ok
}

func Registered() []Kind {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Kind, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}
