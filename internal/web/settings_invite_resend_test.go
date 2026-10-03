package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

var inviteLink = regexp.MustCompile(`/invite/([A-Za-z0-9_-]{20,})`)

// Resend replaces the invitation's token. The new link has to reach the
// admin (production sends no mail), or resending only kills the link the
// invitee already has. The guest's projects travel with it, and accepting
// the new link lists the guest on them.
func TestInvitations_ResendShowsTheNewLink(t *testing.T) {
	e := newQAEnv(t, "acme-resend")
	admin, _ := e.session(t, "admin@acme.example", "admin")
	portal := e.project(t, "Portal")
	_, body := e.post(t, admin, "/settings/members/invite", url.Values{"email": {"client@bank.example"}, "role": {"guest"}, "project_id": {portal.String()}}, true)
	first := inviteLink.FindStringSubmatch(body)
	if first == nil {
		t.Fatalf("invite shows no link:\n%s", body)
	}
	old, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(first[1]))
	if err != nil {
		t.Fatal(err)
	}

	resp, body := e.post(t, admin, "/settings/members/invitations/"+old.ID.String()+"/resend", nil, true)
	second := inviteLink.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || second == nil || second[1] == first[1] {
		t.Fatalf("resend = %d, no new link:\n%s", resp.StatusCode, body)
	}
	if _, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(first[1])); err == nil {
		t.Errorf("the old link still works")
	}
	inv, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(second[1]))
	if err != nil {
		t.Fatalf("new link: %v", err)
	}
	email := "client@bank.example"
	u, _ := global.CreateUser(context.Background(), e.db, global.NewUser{Email: &email, EmailVerified: true})
	if err := global.AcceptInvitation(context.Background(), e.db, inv.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	guest := seedSession(t, e.db, e.orgID, u.ID)
	if status, _ := e.get(t, guest, e.projectPath(t, portal)+"/qa"); status != http.StatusOK {
		t.Errorf("guest from the resent link on its project = %d; want 200", status)
	}
	// A link by the project's id, as mail sent before slug URLs carried,
	// still lands there.
	resp, err = e.app.Test(reqWithSession(http.MethodGet, e.host, "/projects/"+portal.String()+"/qa", guest))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != e.projectPath(t, portal)+"/qa" {
		t.Errorf("guest by project id = %d → %q; want 301 to the slug URL", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// An invitation id belongs to one organization: another organization's
// admin can neither resend nor revoke it.
func TestInvitations_ResendAndRevokeStayInTheOrganization(t *testing.T) {
	e := newQAEnv(t, "acme-invite-scope")
	admin, _ := e.session(t, "admin@acme.example", "admin")
	_, body := e.post(t, admin, "/settings/members/invite", url.Values{"email": {"new@acme.example"}, "role": {"member"}}, true)
	m := inviteLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no link:\n%s", body)
	}
	inv, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	other := newQAEnvOnApp(t, e, "other-invite-scope")
	for _, action := range []string{"resend", "revoke"} {
		resp, _ := other.post(t, other.owner, "/settings/members/invitations/"+inv.ID.String()+"/"+action, nil, false)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s from another organization = %d; want 404", action, resp.StatusCode)
		}
	}
	if _, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(m[1])); err != nil {
		t.Errorf("the invitation no longer works after another organization's attempts: %v", err)
	}
	if !strings.Contains(m[0], "/invite/") {
		t.Fatal(m[0])
	}
}

// newQAEnvOnApp is a second organization on e's app and database.
func newQAEnvOnApp(t *testing.T, e qaEnv, sub string) qaEnv {
	t.Helper()
	orgID, ownerID := seedMember(t, e.db, sub, "owner")
	return qaEnv{
		app: e.app, db: e.db, orgID: orgID, s: tenant.FromRun(e.db, orgID),
		host: sub + "." + dashboardTestDomain, owner: seedSession(t, e.db, orgID, ownerID),
	}
}
