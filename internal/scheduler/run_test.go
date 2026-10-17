package scheduler_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
)

func TestDefaultExecute_NoRegisteredCollectorFailsAsConfigError(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, connID, bindingID := seedFixture(t, db, "acme")

	raw := make([]byte, crypto.DEKSize)
	kek, err := crypto.LoadKEK(base64.StdEncoding.EncodeToString(raw), 1)
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}

	execute := scheduler.DefaultExecute(db, kek, netguard.Config{}, scheduler.NewLimiters(10, 10))
	_, err = execute(context.Background(), &global.SyncRun{
		OrgID: orgID, ConnectionID: connID, BindingID: bindingID, Kind: "not-a-registered-kind",
	})
	if err == nil {
		t.Fatal("expected an error for an unregistered kind")
	}
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}
