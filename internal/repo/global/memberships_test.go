package global_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func TestEnsureMembership_InsertsOnceNeverDowngrades(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	var orgID uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('ens', 'Ens') RETURNING id`)
	u, _ := global.CreateUser(ctx, db, global.NewUser{Email: ptr("Person@Example.com"), EmailVerified: true})

	created, err := global.EnsureMembership(ctx, db, orgID, u.ID, "admin", "active")
	if err != nil || !created {
		t.Fatalf("first = %v, %v", created, err)
	}
	created, _ = global.EnsureMembership(ctx, db, orgID, u.ID, "guest", "suspended")
	if created {
		t.Fatal("second call created again")
	}
	m, _ := global.ActiveMembership(ctx, db, u.ID, orgID)
	if m == nil || m.RoleKey != "admin" {
		t.Fatalf("membership = %+v; want admin kept", m)
	}
	found, _ := global.UserByEmail(ctx, db, "person@EXAMPLE.com")
	if found == nil || found.ID != u.ID {
		t.Fatalf("UserByEmail = %+v", found)
	}
	missing, err := global.UserByEmail(ctx, db, "nobody@example.com")
	if missing != nil || err != nil {
		t.Fatalf("missing = %+v, %v; want nil, nil", missing, err)
	}
}

func ptr(s string) *string { return &s }
