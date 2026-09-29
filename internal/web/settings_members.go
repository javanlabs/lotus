package web

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// invitationRow flattens global.Invitation for the pending-invites panel.
type invitationRow struct {
	ID        string
	Email     string
	RoleKey   string
	ExpiresAt string
}

func settingsMembersPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		members, err := global.ListMembersForOrg(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		invitations, err := global.ListPendingInvitationsForOrg(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		inviteForm := buildInviteForm("", "member", "")
		if inviteForm.Projects, err = inviteProjects(c, nil); err != nil {
			return writeRepoErr(c, err)
		}
		invRows := make([]invitationRow, 0, len(invitations))
		for _, inv := range invitations {
			invRows = append(invRows, invitationRow{
				ID: inv.ID.String(), Email: inv.Email, RoleKey: inv.RoleKey,
				ExpiresAt: inv.ExpiresAt.Local().Format("2006-01-02"),
			})
		}

		nav := buildNav("members", len(projects))
		nav.Trail = []httpx.NavCrumb{{Label: "Settings"}}
		c.Type("html")
		return renderPage(c, d, "pages/settings_members.html", httpx.ViewData{
			Title: "Members", Org: org, User: user, Nav: nav,
			Data: struct {
				SettingsNav   []settingsNavItem
				Members       []global.MemberRow
				Invitations   []invitationRow
				Roles         []string
				CanInvite     bool
				CanChangeRole bool
				CanRemove     bool
				Self          uuid.UUID
				ActorRole     string
				InviteForm    inviteFormView
			}{
				SettingsNav: buildSettingsNav("members"), Members: members, Invitations: invRows,
				InviteForm: inviteForm,
				Roles:      []string{"admin", "member", "viewer", "guest"},
				CanInvite:  chrome.Principal.Can("member.invite"), CanChangeRole: chrome.Principal.Can("member.role.change"),
				CanRemove: chrome.Principal.Can("member.remove"), Self: chrome.Principal.UserID, ActorRole: chrome.Principal.Role,
			},
		})
	}
}

// inviteMemberUI reuses task 1.6/1.7's own invitation machinery
// (global.CreateInvitation, the same per-org/per-email rate limits) — this
// is simply the settings-page entry point onboarding's own invite screen
// doesn't cover: inviting someone into an *existing*, already-running
// organization rather than during initial signup.
// inviteFormView is the Invite-a-member modal's body. Unlike a
// connection's form it never changes shape, so it exists only so a
// rejected submit can come back with the address already typed.
type inviteFormView struct {
	Email    string
	Role     string
	Error    string
	Projects []inviteProjectOption // offered for a guest
	Link     string                // set once the invitation exists: the link to share
}

type inviteProjectOption struct {
	ID      uuid.UUID
	Name    string
	Checked bool
}

var inviteRoles = map[string]bool{"admin": true, "member": true, "viewer": true, "guest": true}

func buildInviteForm(email, role, errMsg string) inviteFormView {
	if !inviteRoles[role] {
		role = "member"
	}
	return inviteFormView{Email: email, Role: role, Error: errMsg}
}

// inviteProjects lists the organization's live projects for the guest
// picker, ticking chosen.
func inviteProjects(c *fiber.Ctx, chosen map[uuid.UUID]bool) ([]inviteProjectOption, error) {
	s, err := mustScope(c)
	if err != nil {
		return nil, err
	}
	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return nil, err
	}
	out := make([]inviteProjectOption, 0, len(projects))
	for _, p := range projects {
		out = append(out, inviteProjectOption{ID: p.ID, Name: p.Name, Checked: chosen[p.ID]})
	}
	return out, nil
}

// rejectInviteForm mirrors rejectConnectionForm: htmx gets the form back
// inside the still-open modal (200, since htmx does not swap 4xx), a
// plain post keeps the error the handler always returned.
func rejectInviteForm(c *fiber.Ctx, d Deps, email, role, msg string, status int) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	f := buildInviteForm(email, role, msg)
	chosen := map[uuid.UUID]bool{}
	for _, raw := range c.Context().PostArgs().PeekMulti("project_id") {
		if id, err := uuid.Parse(string(raw)); err == nil {
			chosen[id] = true
		}
	}
	projects, err := inviteProjects(c, chosen)
	if err != nil {
		return writeRepoErr(c, err)
	}
	f.Projects = projects
	c.Type("html")
	return d.Renderer.RenderNamed(c, "invite_form", f)
}

// inviteMemberUI invites someone into this organization through
// authn.SendInvitation — the same escalation guard, rate limits and email
// as onboarding's invite screen. The answer shows the invitation link, so
// it can be shared even where no mail is configured (a client doing UAT).
// A guest invitation names at least one project of this organization.
func inviteMemberUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		email := strings.ToLower(strings.TrimSpace(c.FormValue("email")))
		role := c.FormValue("role")
		if email == "" || !inviteRoles[role] {
			return rejectInviteForm(c, d, email, role, "A valid email and role are required.", fiber.StatusUnprocessableEntity)
		}
		var projects []uuid.UUID
		if role == "guest" {
			for _, raw := range c.Context().PostArgs().PeekMulti("project_id") {
				id, err := uuid.Parse(string(raw))
				if err == nil {
					_, err = repo.ProjectByID(c.Context(), s, id)
				}
				if err != nil {
					return rejectInviteForm(c, d, email, role, "Choose projects of this organization.", fiber.StatusUnprocessableEntity)
				}
				projects = append(projects, id)
			}
			if len(projects) == 0 {
				return rejectInviteForm(c, d, email, role, "Choose at least one project for a guest.", fiber.StatusUnprocessableEntity)
			}
		}
		org, _ := tenant.OrgFromContext(c)
		link, err := authn.SendInvitation(c.Context(), authn.Deps{DB: d.DB, Cfg: d.Cfg, Mailer: d.Mailer}, authn.SendInvitationParams{
			OrgID: chrome.OrgID, OrgName: org.Name, ActorID: chrome.Principal.UserID, ActorRole: chrome.Principal.Role,
			Email: email, RoleKey: role, ProjectIDs: projects,
		})
		switch {
		case errors.Is(err, global.ErrInvitationPending):
			return rejectInviteForm(c, d, email, role, "An invitation to this email is already pending.", fiber.StatusConflict)
		case errors.Is(err, authn.ErrInviteRateLimited):
			return rejectInviteForm(c, d, email, role, "Too many invitations sent recently.", fiber.StatusTooManyRequests)
		case errors.Is(err, authn.ErrCannotInviteOwner):
			return rejectInviteForm(c, d, email, role, "Only an owner can invite an owner.", fiber.StatusForbidden)
		case err != nil:
			return writeRepoErr(c, err)
		}
		setFlash(c, "member.invited", email) // shown once the dialog's Done reloads the page
		if c.Get("HX-Request") != "" {
			c.Type("html")
			return d.Renderer.RenderNamed(c, "invite_form", inviteFormView{Email: email, Role: role, Link: link})
		}
		return redirectAfterMutation(c, "/settings/members")
	}
}

func newInvitationToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

func resendInvitationUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		// The old link stops working, so the new one has to reach the
		// admin: without mail it is the only way in.
		email, link, err := authn.ResendInvitation(c.Context(), authn.Deps{DB: d.DB, Cfg: d.Cfg, Mailer: d.Mailer}, org.ID, org.Name, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fiber.ErrNotFound
		case errors.Is(err, authn.ErrInviteRateLimited):
			return qaFormError(c, d, fiber.StatusTooManyRequests, "Too many invitations sent recently.")
		case err != nil:
			return writeRepoErr(c, err)
		}
		setFlash(c, "invitation.resent")
		if c.Get("HX-Request") != "" {
			c.Type("html")
			return d.Renderer.RenderNamed(c, "invite_resent", fiber.Map{"Email": email, "Link": link})
		}
		return c.Redirect("/settings/members", fiber.StatusFound)
	}
}

func revokeInvitationUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		if err := global.RevokeInvitation(c.Context(), d.DB, org.ID, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.ErrNotFound
			}
			return writeRepoErr(c, err)
		}
		setFlash(c, "invitation.revoked")
		return c.Redirect("/settings/members", fiber.StatusFound)
	}
}

// changeMemberRoleUI enforces task 1.4's CanChangeRole guard in the
// handler, not just via the coarser authz.Require("member.role.change")
// middleware — an Admin has that permission but still can't touch an
// Owner or promote anyone to Owner (authz.CanChangeRole's own doc
// comment), so the middleware alone isn't the whole story here.
func changeMemberRoleUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		targetID, err := uuid.Parse(c.Params("user_id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		newRole := c.FormValue("role")
		if newRole != "owner" && !inviteRoles[newRole] {
			return fiber.NewError(fiber.StatusBadRequest, "unknown role")
		}
		target, err := global.ActiveMembership(c.Context(), d.DB, targetID, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if !authz.CanChangeRole(chrome.Principal.Role, target.RoleKey, newRole) {
			return fiber.NewError(fiber.StatusForbidden, "not allowed to make this role change")
		}
		roleID, err := roleIDForKey(c, d.DB, newRole)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := global.UpdateMembershipRole(c.Context(), d.DB, chrome.OrgID, targetID, roleID); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "member.role")
		return c.Redirect("/settings/members", fiber.StatusFound)
	}
}

func removeMemberUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		targetID, err := uuid.Parse(c.Params("user_id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := global.RemoveMembership(c.Context(), d.DB, chrome.OrgID, targetID); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "member.removed")
		return c.Redirect("/settings/members", fiber.StatusFound)
	}
}

// roleIDForKey looks up one of the 4 fixed system roles' id by key — the
// same "org_id IS NULL" system-role lookup CreateOrgWithOwner/
// AcceptInvitation already do inline; pulled out here since this is the
// third call site.
func roleIDForKey(c *fiber.Ctx, db *sqlx.DB, key string) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.GetContext(c.Context(), &id, `SELECT id FROM roles WHERE key = $1 AND org_id IS NULL`, key)
	return id, err
}

func registerMemberSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/settings/members", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), settingsMembersPage(d))
	app.Post("/settings/members/invite", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("member.invite"), inviteMemberUI(d))
	app.Post("/settings/members/invitations/:id/resend", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("member.invite"), resendInvitationUI(d))
	app.Post("/settings/members/invitations/:id/revoke", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("member.invite"), revokeInvitationUI(d))
	app.Post("/settings/members/:user_id/role", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("member.role.change"), changeMemberRoleUI(d))
	app.Post("/settings/members/:user_id/remove", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("member.remove"), removeMemberUI(d))
}
