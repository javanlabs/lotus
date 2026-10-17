package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// inviteAndAccept invites email as a guest to projects through Settings ->
// Members (as admin), then accepts the link it shows as a new user, and
// returns that user's session.
func inviteAndAccept(t *testing.T, e qaEnv, admin, email string, projects ...uuid.UUID) (string, uuid.UUID, string) {
	t.Helper()
	form := url.Values{"email": {email}, "role": {"guest"}}
	for _, p := range projects {
		form.Add("project_id", p.String())
	}
	resp, body := e.post(t, admin, "/settings/members/invite", form, true)
	m := regexp.MustCompile(`/invite/([A-Za-z0-9_-]+)`).FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("invite = %d, no link:\n%s", resp.StatusCode, body)
	}
	inv, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(m[1]))
	if err != nil {
		t.Fatalf("invitation: %v", err)
	}
	u, _ := global.CreateUser(context.Background(), e.db, global.NewUser{Email: &email, EmailVerified: true})
	if err := global.AcceptInvitation(context.Background(), e.db, inv.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	return seedSession(t, e.db, e.orgID, u.ID), u.ID, m[0]
}

func TestGuests_InvitedToTwoProjectsSeesExactlyThose(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-guests")
	admin, adminID := e.session(t, "admin@acme.example", "admin")
	a, b := e.project(t, "Portal"), e.project(t, "Mobile")
	e.project(t, "Internal billing")

	guest, _, _ := inviteAndAccept(t, e, admin, "client@bank.example", a, b)
	admin = e.relogin(t, adminID)
	_, body := e.get(t, guest, "/qa")
	if !strings.Contains(body, "Portal") || !strings.Contains(body, "Mobile") || strings.Contains(body, "Internal billing") {
		t.Fatalf("guest /qa:\n%s", body)
	}
	resp, body := e.post(t, admin, "/settings/members/invite", url.Values{"email": {"nobody@bank.example"}, "role": {"guest"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Choose at least one project for a guest.") {
		t.Fatalf("guest without projects = %d:\n%s", resp.StatusCode, body)
	}
	otherOrg, _ := seedMember(t, e.db, "other-guests", "owner")
	foreign, _ := repo.CreateProject(context.Background(), tenant.FromRun(e.db, otherOrg), repo.NewProject{Slug: "f", Name: "Foreign"})
	resp, body = e.post(t, admin, "/settings/members/invite", url.Values{"email": {"x@bank.example"}, "role": {"guest"}, "project_id": {foreign.ID.String()}}, true)
	if !strings.Contains(body, "Choose projects of this organization.") {
		t.Fatalf("project of another organization = %d:\n%s", resp.StatusCode, body)
	}
}

func TestGuests_ProjectEndpointsAddAndRemove(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-guests-panel")
	admin, adminID := e.session(t, "admin@acme.example", "admin")
	_, memberID := e.session(t, "qa@acme.example", "member")
	a, b := e.project(t, "Portal"), e.project(t, "Mobile")
	guest, guestID, _ := inviteAndAccept(t, e, admin, "client@bank.example", a)
	admin, member := e.relogin(t, adminID), e.relogin(t, memberID)

	// The project page no longer carries a guests panel (removed
	// 2026-10-04); the endpoints behind it still work.
	status, body := e.get(t, admin, e.projectPath(t, b))
	if status != http.StatusOK || strings.Contains(body, "Clients who see this project") || strings.Contains(body, `action="/projects/`+b.String()+`/guests"`) {
		t.Fatalf("project page for admin = %d, still shows the guests panel", status)
	}
	if resp, _ := e.post(t, admin, e.projectPath(t, b)+"/guests", url.Values{"user_id": {guestID.String()}}, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("add guest = %d", resp.StatusCode)
	}
	if _, body = e.get(t, guest, "/qa"); !strings.Contains(body, "Mobile") {
		t.Fatal("added project not visible to the guest")
	}
	if resp, _ := e.post(t, admin, e.projectPath(t, a)+"/guests/"+guestID.String()+"/remove", nil, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("remove guest = %d", resp.StatusCode)
	}
	if status, _ := e.get(t, guest, e.projectPath(t, a)+"/qa"); status != http.StatusNotFound {
		t.Fatalf("removed project = %d; want 404 at once", status)
	}
	if n := auditCount(t, e.db, e.orgID, "project_member.add") + auditCount(t, e.db, e.orgID, "project_member.remove"); n != 2 {
		t.Fatalf("audit rows = %d; want 2", n)
	}
	// A member cannot manage guests; only guests can be listed.
	if resp, _ := e.post(t, member, e.projectPath(t, b)+"/guests", url.Values{"user_id": {guestID.String()}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member add = %d; want 403", resp.StatusCode)
	}
	_, otherID := e.session(t, "other@acme.example", "member")
	admin = e.relogin(t, adminID)
	if resp, _ := e.post(t, admin, e.projectPath(t, b)+"/guests", url.Values{"user_id": {otherID.String()}}, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("listing a non-guest = %d; want 404", resp.StatusCode)
	}
}

func TestGuests_AdminChangesGuestToMemberAndBack(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-guests-role")
	admin, adminID := e.session(t, "admin@acme.example", "admin")
	p := e.project(t, "Portal")
	_, guestID, _ := inviteAndAccept(t, e, admin, "client@bank.example", p)
	path := "/settings/members/" + guestID.String() + "/role"
	for _, role := range []string{"member", "guest"} {
		admin = e.relogin(t, adminID)
		resp, _ := e.post(t, admin, path, url.Values{"role": {role}}, false)
		m, _ := global.ActiveMembership(context.Background(), e.db, guestID, e.orgID)
		if resp.StatusCode != http.StatusFound || m.RoleKey != role {
			t.Fatalf("to %s = %d, role %s", role, resp.StatusCode, m.RoleKey)
		}
	}
	if _, body := e.get(t, e.relogin(t, adminID), "/settings/members"); !strings.Contains(body, `"Value":"guest"`) && !strings.Contains(body, ">Guest<") {
		t.Fatal("members page does not offer the guest role")
	}
}

// Removing a guest from the organization ends its project listings: invited
// back later to one project, it sees that project only.
func TestGuests_RemovedFromTheOrganizationLosesItsProjects(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-guest-removed")
	admin, adminID := e.session(t, "admin@acme.example", "admin")
	a, b, c := e.project(t, "Portal"), e.project(t, "Mobile"), e.project(t, "Billing")
	_, guestID, _ := inviteAndAccept(t, e, admin, "client@bank.example", a, b)

	admin = e.relogin(t, adminID)
	if resp, _ := e.post(t, admin, "/settings/members/"+guestID.String()+"/remove", nil, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("remove guest = %d", resp.StatusCode)
	}
	admin = e.relogin(t, adminID)
	_, body := e.post(t, admin, "/settings/members/invite", url.Values{"email": {"client@bank.example"}, "role": {"guest"}, "project_id": {c.String()}}, true)
	m := regexp.MustCompile(`/invite/([A-Za-z0-9_-]+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("re-invite shows no link:\n%s", body)
	}
	inv, err := global.InvitationByTokenHash(context.Background(), e.db, sha256HexForProjectsTest(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if err := global.AcceptInvitation(context.Background(), e.db, inv.ID, guestID); err != nil {
		t.Fatal(err)
	}
	guest := seedSession(t, e.db, e.orgID, guestID)
	for project, want := range map[uuid.UUID]int{a: http.StatusNotFound, b: http.StatusNotFound, c: http.StatusOK} {
		if status, _ := e.get(t, guest, e.projectPath(t, project)+"/qa"); status != want {
			t.Errorf("re-invited guest on %s = %d; want %d", project, status, want)
		}
	}
}
