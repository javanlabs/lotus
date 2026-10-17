package repo_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Replacing a binding retires the old one, takes its kind out of the
// component's projection (the old value must not keep counting), and
// leaves exactly one live binding of the kind.
func TestReplaceBinding_RetiresTheOldOneAndItsProjectedValue(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, oldID, _ := seedTrivyBindingForNotes(t, db, "acme-replace")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()

	old, err := repo.BindingByID(ctx, s, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertComponentMetric(ctx, s, old.ComponentID, "trivy", collector.Headline{Status: collector.StatusFail}); err != nil {
		t.Fatal(err)
	}

	in := repo.NewBinding{ComponentID: old.ComponentID, ConnectionID: old.ConnectionID, Kind: "trivy", ExternalKey: "proj/api", Config: json.RawMessage(`{}`)}
	if _, err := repo.CreateBinding(ctx, s, in); !errors.Is(err, repo.ErrBindingTargetTaken) {
		t.Fatalf("second trivy binding = %v, want ErrBindingTargetTaken", err)
	}
	fresh, err := repo.ReplaceBinding(ctx, s, oldID, in)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	live, err := repo.ActiveBindingForKind(ctx, s, old.ComponentID, "trivy")
	if err != nil || live.ID != fresh.ID || live.ExternalKey != "proj/api" {
		t.Fatalf("live trivy binding = %+v, %v; want the new one", live, err)
	}
	if got, _ := repo.BindingByID(ctx, s, oldID); got.DeletedAt == nil {
		t.Fatal("old binding still live")
	}
	cm, err := repo.GetComponentMetric(ctx, s, old.ComponentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := cm.Metrics()["trivy"]; still || cm.Status != string(collector.StatusUnknown) {
		t.Fatalf("projection after replace: status %s, metrics %s", cm.Status, cm.MetricsJSON)
	}

	// Replacing something that is not the component's live binding of
	// the kind is refused, and changes nothing.
	if _, err := repo.ReplaceBinding(ctx, s, oldID, in); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("replace a retired binding = %v, want ErrNotFound", err)
	}
}

func TestSoftDeleteBinding_DropsItsKindFromTheProjection(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, bindingID, _ := seedTrivyBindingForNotes(t, db, "acme-delete")
	s := tenant.FromRun(db, orgID)
	ctx := context.Background()
	b, _ := repo.BindingByID(ctx, s, bindingID)
	score := 80
	for kind, h := range map[string]collector.Headline{
		"trivy":  {Status: collector.StatusFail},
		"gitlab": {Status: collector.StatusPass, Score: &score},
	} {
		if err := repo.UpsertComponentMetric(ctx, s, b.ComponentID, kind, h); err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.SoftDeleteBinding(ctx, s, bindingID); err != nil {
		t.Fatal(err)
	}
	cm, _ := repo.GetComponentMetric(ctx, s, b.ComponentID)
	if _, still := cm.Metrics()["trivy"]; still {
		t.Fatalf("trivy still projected: %s", cm.MetricsJSON)
	}
	// The rollup is recomputed from what is left: gitlab alone passes.
	if cm.Status != string(collector.StatusPass) || cm.Score == nil || *cm.Score != 80 {
		t.Fatalf("rollup after delete = %s / %v, want pass / 80", cm.Status, cm.Score)
	}
	if err := repo.SoftDeleteBinding(ctx, s, bindingID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}
