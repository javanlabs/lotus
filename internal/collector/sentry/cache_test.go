package sentry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/uyab/lotus/internal/collector"
)

// TestCollect_NineteenBindingsOnOneProjectFetchMetadataOnce is task 3.5's
// own acceptance criterion, quoted directly: "19 binding di satu Sentry
// project ngasilin 1 fetch project metadata per batch, bukan 19."
func TestCollect_NineteenBindingsOnOneProjectFetchMetadataOnce(t *testing.T) {
	var projectHits int
	srv := fakeSentryServer(t, &projectHits)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated"})
	for i := 0; i < 19; i++ {
		if _, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg}); err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
	}
	if projectHits != 1 {
		t.Fatalf("project metadata fetched %d times, want 1 (19 bindings sharing one project must share the cache)", projectHits)
	}
}
