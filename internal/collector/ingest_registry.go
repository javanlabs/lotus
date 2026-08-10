package collector

import "sync"

var (
	ingestorMu sync.RWMutex
	ingestors  = map[Kind]Ingestor{}
)

// RegisterIngestor adds an Ingestor under k — the push-direction
// counterpart to Register, same one-import-and-nothing-else wiring.
// Panics on a duplicate registration for the same reason Register does.
func RegisterIngestor(k Kind, i Ingestor) {
	ingestorMu.Lock()
	defer ingestorMu.Unlock()
	if _, exists := ingestors[k]; exists {
		panic("collector: duplicate ingestor registration for kind " + string(k))
	}
	ingestors[k] = i
}

func GetIngestor(k Kind) (Ingestor, bool) {
	ingestorMu.RLock()
	defer ingestorMu.RUnlock()
	i, ok := ingestors[k]
	return i, ok
}
