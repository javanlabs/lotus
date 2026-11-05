package trivy

import (
	"testing"
	"time"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func TestHeadlineFromSnapshot(t *testing.T) {
	now := time.Now()
	h := HeadlineFromSnapshot(&repo.TrivySnapshot{Source: "registry", ScanFinishedAt: &now, Total: 31, Critical: 1, High: 5}, "critical")
	if h.Score == nil || *h.Score != 49 || h.Status != collector.StatusFail {
		t.Fatalf("headline = %+v", h)
	}
	ns := HeadlineFromSnapshot(&repo.TrivySnapshot{Source: "registry"}, "")
	if ns.Score != nil || ns.Status != collector.StatusUnknown || ns.StateLabel != "Not scanned" {
		t.Fatalf("an unscanned image = %+v", ns)
	}
	clean := HeadlineFromSnapshot(&repo.TrivySnapshot{Source: "push"}, "")
	if clean.Score == nil || *clean.Score != 100 {
		t.Fatalf("a pushed clean report = %+v", clean)
	}
}
