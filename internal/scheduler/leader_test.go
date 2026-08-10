package scheduler_test

import (
	"context"
	"testing"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/scheduler"
)

func TestAcquireLeader_OnlyOneHolderAtATime(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	release1, acquired1, err := scheduler.AcquireLeader(ctx, db)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if !acquired1 {
		t.Fatal("expected the first acquire to succeed")
	}
	defer release1()

	_, acquired2, err := scheduler.AcquireLeader(ctx, db)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if acquired2 {
		t.Fatal("expected a second concurrent acquire to fail while the first holds the lock")
	}

	release1()

	release3, acquired3, err := scheduler.AcquireLeader(ctx, db)
	if err != nil {
		t.Fatalf("acquire 3: %v", err)
	}
	if !acquired3 {
		t.Fatal("expected leadership to be acquirable again once the first holder released it")
	}
	release3()
}
