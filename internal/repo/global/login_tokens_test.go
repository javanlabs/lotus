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

func seedUserAndOrg(t *testing.T, email string) (*sqlx.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	db := dbtest.Open(t)
	var orgID, userID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme','Acme') RETURNING id`); err != nil {
		t.Fatalf("org: %v", err)
	}
	if err := db.Get(&userID,
		`INSERT INTO users (email, name, email_verified_at) VALUES ($1,'T',now()) RETURNING id`, email); err != nil {
		t.Fatalf("user: %v", err)
	}
	return db, orgID, userID
}

func mint(t *testing.T, db *sqlx.DB, userID, orgID uuid.UUID, ttl time.Duration) string {
	t.Helper()
	id := uuid.NewString()
	err := global.CreateLoginToken(context.Background(), db, global.NewLoginToken{
		ID: id, UserID: userID, OrgID: orgID, ReturnPath: "/dashboard",
		ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		t.Fatalf("CreateLoginToken: %v", err)
	}
	return id
}

// TestConsumeLoginToken_IsSingleUse is the property the whole mechanism
// rests on: a link sitting in an inbox, a shell history, or a proxy log
// must stop working the instant it is redeemed once.
func TestConsumeLoginToken_IsSingleUse(t *testing.T) {
	db, orgID, userID := seedUserAndOrg(t, "a@lotus.test")
	id := mint(t, db, userID, orgID, time.Hour)

	tok, err := global.ConsumeLoginToken(context.Background(), db, id)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if tok.UserID != userID || tok.OrgID != orgID || tok.ReturnPath != "/dashboard" {
		t.Fatalf("token = %+v, want the values it was minted with", tok)
	}
	if _, err := global.ConsumeLoginToken(context.Background(), db, id); err != global.ErrLoginTokenInvalid {
		t.Fatalf("second consume: err = %v, want ErrLoginTokenInvalid", err)
	}
}

func TestConsumeLoginToken_ExpiredAndUnknownBothRejected(t *testing.T) {
	db, orgID, userID := seedUserAndOrg(t, "b@lotus.test")
	expired := mint(t, db, userID, orgID, -time.Minute)

	if _, err := global.ConsumeLoginToken(context.Background(), db, expired); err != global.ErrLoginTokenInvalid {
		t.Errorf("expired: err = %v, want ErrLoginTokenInvalid", err)
	}
	if _, err := global.ConsumeLoginToken(context.Background(), db, "never-existed"); err != global.ErrLoginTokenInvalid {
		t.Errorf("unknown: err = %v, want ErrLoginTokenInvalid", err)
	}
}

func TestCountRecentLoginTokens(t *testing.T) {
	db, orgID, userID := seedUserAndOrg(t, "c@lotus.test")
	for i := 0; i < 3; i++ {
		mint(t, db, userID, orgID, time.Hour)
	}
	n, err := global.CountRecentLoginTokens(context.Background(), db, userID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}
}

// TestDevUsers_OnlyListsReservedTestAddresses is the narrowest of the three
// gates around one-click dev login: even reached, it can only ever name an
// account at a domain RFC 2606 reserves, which no real person can hold.
func TestDevUsers_OnlyListsReservedTestAddresses(t *testing.T) {
	db, orgID, devID := seedUserAndOrg(t, "owner"+global.DevUserSuffix)
	var realID uuid.UUID
	if err := db.Get(&realID,
		`INSERT INTO users (email, name, email_verified_at) VALUES ('ceo@javan.co.id','Real',now()) RETURNING id`); err != nil {
		t.Fatalf("real user: %v", err)
	}
	for _, id := range []uuid.UUID{devID, realID} {
		if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id)
			SELECT $1,$2,r.id FROM roles r WHERE r.key='owner' AND r.org_id IS NULL`, orgID, id); err != nil {
			t.Fatalf("membership: %v", err)
		}
	}

	users, err := global.DevUsers(context.Background(), db, orgID)
	if err != nil {
		t.Fatalf("DevUsers: %v", err)
	}
	if len(users) != 1 || users[0].ID != devID {
		t.Fatalf("DevUsers = %+v, want exactly the one @lotus.test account", users)
	}
}
