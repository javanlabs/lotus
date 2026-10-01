package global_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
)

func TestUsersByIDs_KeysTheUsersThatExist(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	ana, _ := global.CreateUser(ctx, db, global.NewUser{Email: ptr("ana@example.com"), Name: "Ana"})
	if got, err := global.UsersByIDs(ctx, db, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids = %v, %v", got, err)
	}
	got, err := global.UsersByIDs(ctx, db, []uuid.UUID{ana.ID, uuid.New()})
	if err != nil || len(got) != 1 || got[ana.ID].Name != "Ana" {
		t.Fatalf("users = %+v, %v; want only Ana", got, err)
	}
}
