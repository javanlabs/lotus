package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func mustUser(t *testing.T, db *sqlx.DB, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Get(&id, `INSERT INTO users (email) VALUES ($1) RETURNING id`, email); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func TestProjectMembers_AddIsIdempotentAndScoped(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-pm")
	_, other := folderTestOrg(t, db, "other-pm")
	p := mustProject(t, s, "portal", nil)
	u := mustUser(t, db, "client@bank.example")

	for i := 0; i < 2; i++ {
		if err := repo.AddProjectMember(ctx, s, p.ID, u); err != nil {
			t.Fatalf("add #%d: %v", i, err)
		}
	}
	ok, err := repo.IsProjectMember(ctx, s, p.ID, u)
	if err != nil || !ok {
		t.Fatalf("IsProjectMember = %v, %v; want true", ok, err)
	}
	ids, _ := repo.ListProjectIDsForMember(ctx, s, u)
	if len(ids) != 1 || ids[0] != p.ID {
		t.Fatalf("ListProjectIDsForMember = %v; want [%v]", ids, p.ID)
	}
	// Another organization cannot see or add to this project.
	if ok, _ := repo.IsProjectMember(ctx, other, p.ID, u); ok {
		t.Fatal("other org sees the membership")
	}
	if err := repo.AddProjectMember(ctx, other, p.ID, u); err != repo.ErrNotFound {
		t.Fatalf("cross-org add = %v; want ErrNotFound", err)
	}
	if err := repo.RemoveProjectMember(ctx, s, p.ID, u); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if ok, _ := repo.IsProjectMember(ctx, s, p.ID, u); ok {
		t.Fatal("still a member after remove")
	}
}
