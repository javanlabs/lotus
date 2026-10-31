package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/crypto"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// A failed sign-in freezes a connection's bindings; a new credential resumes
// them. One turned off on purpose must stay off through both.
func TestFreeze_ANewCredentialResumesOnlyWhatTheFailureStopped(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, _, running := seedMFFixture(t, db, "acme-freeze", "gitlab")
	scope := tenant.FromRun(db, orgID)
	ctx := context.Background()

	binding, err := repo.BindingByID(ctx, scope, running)
	if err != nil {
		t.Fatal(err)
	}
	comp, err := repo.CreateComponent(ctx, scope, repo.NewComponent{ProjectID: mustProjectOf(t, ctx, scope, binding.ComponentID), Slug: "off", Name: "off", Kind: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	off, err := repo.CreateBinding(ctx, scope, repo.NewBinding{ComponentID: comp.ID, ConnectionID: binding.ConnectionID, Kind: "gitlab", ExternalKey: "off/key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetBindingEnabled(ctx, scope, off.ID, false); err != nil {
		t.Fatal(err)
	}

	enabled := func(id uuid.UUID) bool {
		b, err := repo.BindingByID(ctx, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		return b.Enabled
	}

	if err := repo.FreezeBindingsForConnection(ctx, scope, binding.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if enabled(running) {
		t.Fatal("the failure did not stop the running binding")
	}

	dek, err := crypto.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateConnectionSecret(ctx, scope, dek, binding.ConnectionID, "a-new-token"); err != nil {
		t.Fatal(err)
	}
	if !enabled(running) {
		t.Error("a new credential did not resume the frozen binding")
	}
	if enabled(off.ID) {
		t.Error("a new credential turned on a binding someone had turned off")
	}
}

func mustProjectOf(t *testing.T, ctx context.Context, s tenant.Scope, componentID uuid.UUID) uuid.UUID {
	t.Helper()
	c, err := repo.ComponentByID(ctx, s, componentID)
	if err != nil {
		t.Fatal(err)
	}
	return c.ProjectID
}
