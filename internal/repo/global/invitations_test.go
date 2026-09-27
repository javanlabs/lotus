package global_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func projectMembers(t *testing.T, db *sqlx.DB, orgID, userID uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	tx, _ := db.Beginx()
	defer tx.Rollback() //nolint:errcheck
	tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String())
	var ids []uuid.UUID
	if err := tx.Select(&ids, `SELECT project_id FROM project_members WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// A guest invitation carries the projects the guest will see; accepting
// it lists the guest on them (only projects of the inviting organization).
func TestInvitation_GuestProjectsAreAppliedOnAccept(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	var orgID, otherOrg, a, b, foreign, inviter uuid.UUID
	db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme-inv', 'Acme') RETURNING id`)
	db.Get(&otherOrg, `INSERT INTO organizations (subdomain, name) VALUES ('other-inv', 'Other') RETURNING id`)
	a, b = seedProject(t, db, orgID, "a"), seedProject(t, db, orgID, "b")
	foreign = seedProject(t, db, otherOrg, "f")
	db.Get(&inviter, `INSERT INTO users (email) VALUES ('admin@acme.example') RETURNING id`)

	invID, err := global.CreateInvitation(ctx, db, global.NewInvitation{
		OrgID: orgID, Email: "client@bank.example", RoleKey: "guest", InvitedBy: inviter,
		TokenHash: "h1", ExpiresAt: time.Now().Add(time.Hour), ProjectIDs: []uuid.UUID{a, foreign},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Resending keeps the projects.
	newID, err := global.ResendInvitation(ctx, db, orgID, invID, "h2", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := global.CreateUser(ctx, db, global.NewUser{Email: ptr("client@bank.example"), EmailVerified: true})
	if err := global.AcceptInvitation(ctx, db, newID, u.ID); err != nil {
		t.Fatal(err)
	}
	got := projectMembers(t, db, orgID, u.ID)
	if len(got) != 1 || !got[a] || got[b] {
		t.Fatalf("guest projects = %v; want only A (B was not chosen, F belongs to another organization)", got)
	}
	if m, _ := global.ActiveMembership(ctx, db, u.ID, orgID); m == nil || m.RoleKey != "guest" {
		t.Fatalf("membership = %+v", m)
	}
}

// seedProject inserts a project under its organization's row-level security.
func seedProject(t *testing.T, db *sqlx.DB, orgID uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	tx.Exec(`SELECT set_config('app.org_id', $1, true)`, orgID.String())
	var id uuid.UUID
	if err := tx.Get(&id, `INSERT INTO projects (org_id, slug, name) VALUES ($1, $2, $2) RETURNING id`, orgID, slug); err != nil {
		t.Fatalf("project %s: %v", slug, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}
