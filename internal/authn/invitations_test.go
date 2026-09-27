package authn_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/config"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

func newInviteTestApp(t *testing.T) (*fiber.App, *sqlx.DB, config.Config, authn.Deps) {
	t.Helper()
	db := dbtest.Open(t)
	cfg := config.Config{BaseDomain: handoffTestDomain, Dev: true, CookieSecure: false}
	resolver := tenant.NewResolver(cfg, db)
	deps := authn.Deps{DB: db, Cfg: cfg, Renderer: testRenderer(t), OAuthProviders: authn.Registry{}, CookieSecure: false}

	app := fiber.New()
	app.Use(resolver.Middleware())
	app.Get("/invite/:token", authn.InviteLanding(deps))
	app.Post("/invite/:token/confirm", authn.InviteConfirm(deps))
	app.Post("/invite/:token/decline", authn.InviteDecline(deps))
	app.Get("/handoff/start", tenant.RequireTenant(), authn.HandoffStart(deps))
	return app, db, cfg, deps
}

// seedInvitedOrg creates an org with an Owner (so escalation-guard tests
// have a legitimate Owner actor to compare against) and returns its id
// along with that Owner's user id — invitations.invited_by is a real FK to
// users, so tests need an actual row, not a random uuid.
func seedInvitedOrg(t *testing.T, db *sqlx.DB, subdomain string) (orgID, ownerUserID uuid.UUID) {
	t.Helper()
	return seedActiveMember(t, db, subdomain)
}

func createTestInvitation(t *testing.T, db *sqlx.DB, orgID, invitedBy uuid.UUID, email, roleKey string, ttl time.Duration) (uuid.UUID, string) {
	t.Helper()
	raw := "invite-" + uuid.New().String()
	id, err := global.CreateInvitation(t.Context(), db, global.NewInvitation{
		OrgID: orgID, Email: email, RoleKey: roleKey, InvitedBy: invitedBy,
		TokenHash: sha256HexForTest(raw), ExpiresAt: time.Now().Add(ttl),
	})
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	return id, raw
}

func TestSendInvitation_EscalationGuard(t *testing.T) {
	_, db, _, deps := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	ctx := context.Background()

	_, err := authn.SendInvitation(ctx, deps, authn.SendInvitationParams{
		OrgID: orgID, OrgName: "Acme", ActorID: ownerID, ActorRole: "admin",
		Email: "newowner@example.com", RoleKey: "owner",
	})
	if err != authn.ErrCannotInviteOwner {
		t.Fatalf("err = %v, want ErrCannotInviteOwner", err)
	}

	_, err = authn.SendInvitation(ctx, deps, authn.SendInvitationParams{
		OrgID: orgID, OrgName: "Acme", ActorID: ownerID, ActorRole: "owner",
		Email: "newowner2@example.com", RoleKey: "owner",
	})
	if err != nil {
		t.Fatalf("owner inviting owner should succeed: %v", err)
	}
}

func TestSendInvitation_RateLimitedPerOrg(t *testing.T) {
	_, db, _, deps := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		_, err := authn.SendInvitation(ctx, deps, authn.SendInvitationParams{
			OrgID: orgID, OrgName: "Acme", ActorID: ownerID, ActorRole: "owner",
			Email: uuid.New().String() + "@example.com", RoleKey: "member",
		})
		if err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	_, err := authn.SendInvitation(ctx, deps, authn.SendInvitationParams{
		OrgID: orgID, OrgName: "Acme", ActorID: ownerID, ActorRole: "owner",
		Email: "onemore@example.com", RoleKey: "member",
	})
	if err != authn.ErrInviteRateLimited {
		t.Fatalf("err = %v, want ErrInviteRateLimited", err)
	}
}

func TestInvitation_ExpiredUsedRevokedAllRejected(t *testing.T) {
	app, db, cfg, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")

	_, expiredToken := createTestInvitation(t, db, orgID, ownerID, "a@example.com", "member", -time.Hour)
	usedID, usedToken := createTestInvitation(t, db, orgID, ownerID, "b@example.com", "member", 24*time.Hour)
	revokedID, revokedToken := createTestInvitation(t, db, orgID, ownerID, "c@example.com", "member", 24*time.Hour)

	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('user@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := global.AcceptInvitation(t.Context(), db, usedID, userID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := global.RevokeInvitation(t.Context(), db, orgID, revokedID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	for name, token := range map[string]string{"expired": expiredToken, "used": usedToken, "revoked": revokedToken} {
		t.Run(name, func(t *testing.T) {
			req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/invite/"+token)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "not found") && !strings.Contains(string(body), "invalid") {
				t.Fatalf("%s token: expected invalid/not-found page, got: %s", name, body)
			}
		})
	}
}

func TestInvitation_AdminRoleMismatchHardRejected(t *testing.T) {
	app, db, cfg, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	_, token := createTestInvitation(t, db, orgID, ownerID, "invited@example.com", "admin", 24*time.Hour)

	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('different@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	sessionRaw := seedGlobalSession(t, db, userID)

	req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/invite/"+token)
	req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Wrong account") {
		t.Fatalf("expected hard-rejection page, got: %s", body)
	}

	// Even hitting confirm directly must not accept it.
	confirmReq := hostReq(http.MethodPost, "auth."+cfg.BaseDomain, "/invite/"+token+"/confirm")
	confirmReq.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	confirmResp, err := app.Test(confirmReq)
	if err != nil {
		t.Fatalf("confirm request: %v", err)
	}
	if confirmResp.StatusCode == http.StatusFound {
		t.Fatal("expected admin-role mismatch to be rejected even via direct confirm POST")
	}

	var accepted bool
	if err := db.Get(&accepted, `SELECT accepted_at IS NOT NULL FROM invitations WHERE token_hash = $1`, sha256HexForTest(token)); err != nil {
		t.Fatalf("check accepted: %v", err)
	}
	if accepted {
		t.Fatal("admin invite must not have been accepted")
	}
}

func TestInvitation_MemberRoleMismatchRequiresConfirm(t *testing.T) {
	app, db, cfg, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	_, token := createTestInvitation(t, db, orgID, ownerID, "invited@example.com", "member", 24*time.Hour)

	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('different2@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	sessionRaw := seedGlobalSession(t, db, userID)
	authCookie := &http.Cookie{Name: "lotus_auth", Value: sessionRaw}

	landingReq := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/invite/"+token)
	landingReq.AddCookie(authCookie)
	landingResp, err := app.Test(landingReq)
	if err != nil {
		t.Fatalf("landing: %v", err)
	}
	body, _ := io.ReadAll(landingResp.Body)
	if !strings.Contains(string(body), "Accept anyway") {
		t.Fatalf("expected soft-mismatch confirm page, got: %s", body)
	}

	confirmReq := hostReq(http.MethodPost, "auth."+cfg.BaseDomain, "/invite/"+token+"/confirm")
	confirmReq.AddCookie(authCookie)
	confirmResp, err := app.Test(confirmReq)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmResp.StatusCode != http.StatusFound {
		t.Fatalf("confirm status = %d, want 302", confirmResp.StatusCode)
	}

	var accepted bool
	if err := db.Get(&accepted, `SELECT accepted_at IS NOT NULL FROM invitations WHERE token_hash = $1`, sha256HexForTest(token)); err != nil {
		t.Fatalf("check accepted: %v", err)
	}
	if !accepted {
		t.Fatal("expected invitation to be accepted after explicit confirm")
	}
}

func TestInvitation_MatchingVerifiedEmailAcceptsImmediately(t *testing.T) {
	app, db, cfg, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	_, token := createTestInvitation(t, db, orgID, ownerID, "alice@example.com", "member", 24*time.Hour)

	var userID uuid.UUID
	if err := db.Get(&userID, `INSERT INTO users (email, email_verified_at) VALUES ('alice@example.com', now()) RETURNING id`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	sessionRaw := seedGlobalSession(t, db, userID)

	req := hostReq(http.MethodGet, "auth."+cfg.BaseDomain, "/invite/"+token)
	req.AddCookie(&http.Cookie{Name: "lotus_auth", Value: sessionRaw})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 302: %s", resp.StatusCode, body)
	}

	var membershipCount int
	if err := db.Get(&membershipCount, `SELECT count(*) FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		t.Fatalf("check membership: %v", err)
	}
	if membershipCount != 1 {
		t.Fatalf("membership count = %d, want 1", membershipCount)
	}
}

func TestInvitation_DeclineSetsDeclinedAt(t *testing.T) {
	app, db, cfg, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	_, token := createTestInvitation(t, db, orgID, ownerID, "declineme@example.com", "member", 24*time.Hour)

	req := hostReq(http.MethodPost, "auth."+cfg.BaseDomain, "/invite/"+token+"/decline")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var declined bool
	if err := db.Get(&declined, `SELECT declined_at IS NOT NULL FROM invitations WHERE token_hash = $1`, sha256HexForTest(token)); err != nil {
		t.Fatalf("check declined: %v", err)
	}
	if !declined {
		t.Fatal("expected declined_at to be set")
	}
}

func TestResendInvitation_InvalidatesOldToken(t *testing.T) {
	_, db, _, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")
	oldID, _ := createTestInvitation(t, db, orgID, ownerID, "resend@example.com", "member", 24*time.Hour)

	newRaw := "new-token"
	newID, err := global.ResendInvitation(t.Context(), db, orgID, oldID, sha256HexForTest(newRaw), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if newID == oldID {
		t.Fatal("resend should produce a new invitation id")
	}

	var oldRevoked bool
	if err := db.Get(&oldRevoked, `SELECT revoked_at IS NOT NULL FROM invitations WHERE id = $1`, oldID); err != nil {
		t.Fatalf("check old: %v", err)
	}
	if !oldRevoked {
		t.Fatal("expected old invitation to be revoked by resend")
	}

	// The old row is revoked, so a resend-of-a-resend must fail cleanly.
	if _, err := global.ResendInvitation(t.Context(), db, orgID, oldID, sha256HexForTest("another"), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected resending an already-revoked invitation to fail")
	}
}

func TestCreateInvitation_OnePendingPerOrgEmail(t *testing.T) {
	_, db, _, _ := newInviteTestApp(t)
	orgID, ownerID := seedInvitedOrg(t, db, "acme")

	_, err := global.CreateInvitation(t.Context(), db, global.NewInvitation{
		OrgID: orgID, Email: "dup@example.com", RoleKey: "member", InvitedBy: ownerID,
		TokenHash: "hash1", ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	_, err = global.CreateInvitation(t.Context(), db, global.NewInvitation{
		OrgID: orgID, Email: "dup@example.com", RoleKey: "admin", InvitedBy: ownerID,
		TokenHash: "hash2", ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != global.ErrInvitationPending {
		t.Fatalf("err = %v, want ErrInvitationPending", err)
	}
}
