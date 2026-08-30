package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

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
		invRows := make([]invitationRow, 0, len(invitations))
		for _, inv := range invitations {
			invRows = append(invRows, invitationRow{
				ID: inv.ID.String(), Email: inv.Email, RoleKey: inv.RoleKey,
				ExpiresAt: inv.ExpiresAt.Local().Format("2006-01-02"),
			})
		}

		nav := buildNav("", len(projects))
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
				InviteForm: buildInviteForm("", "member", ""),
				Roles:      []string{"admin", "member", "viewer"},
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
	Email string
	Role  string
	Error string
}

func buildInviteForm(email, role, errMsg string) inviteFormView {
	if role != "admin" && role != "member" && role != "viewer" {
		role = "member"
	}
	return inviteFormView{Email: email, Role: role, Error: errMsg}
}

// rejectInviteForm mirrors rejectConnectionForm: htmx gets the form back
// inside the still-open modal (200, since htmx does not swap 4xx), a
// plain post keeps the error the handler always returned.
func rejectInviteForm(c *fiber.Ctx, d Deps, email, role, msg string, status int) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(status, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "invite_form", buildInviteForm(email, role, msg))
}

func inviteMemberUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		email := strings.ToLower(strings.TrimSpace(c.FormValue("email")))
		role := c.FormValue("role")
		if email == "" || (role != "admin" && role != "member" && role != "viewer") {
			return rejectInviteForm(c, d, email, role, "A valid email and role are required.", fiber.StatusUnprocessableEntity)
		}
		since := time.Now().Add(-24 * time.Hour)
		if n, err := global.CountRecentInvitationsByOrg(c.Context(), d.DB, chrome.OrgID, since); err != nil {
			return writeRepoErr(c, err)
		} else if n >= 50 {
			return rejectInviteForm(c, d, email, role, "Too many invitations sent recently.", fiber.StatusTooManyRequests)
		}

		token, hash, err := newInvitationToken()
		if err != nil {
			return writeRepoErr(c, err)
		}
		if _, err := global.CreateInvitation(c.Context(), d.DB, global.NewInvitation{
			OrgID: chrome.OrgID, Email: email, RoleKey: role, InvitedBy: chrome.Principal.UserID,
			TokenHash: hash, ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		}); err != nil {
			if errors.Is(err, global.ErrInvitationPending) {
				return rejectInviteForm(c, d, email, role, "An invitation to this email is already pending.", fiber.StatusConflict)
			}
			return writeRepoErr(c, err)
		}
		_ = token // sent by email in the onboarding flow (task 1.6); this settings-page path has no mailer wiring yet, matching resend's own scope below
		setFlash(c, "member.invited", email)
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
		_, hash, err := newInvitationToken()
		if err != nil {
			return writeRepoErr(c, err)
		}
		if _, err := global.ResendInvitation(c.Context(), d.DB, id, hash, time.Now().Add(7*24*time.Hour)); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "invitation.resent")
		return c.Redirect("/settings/members", fiber.StatusFound)
	}
}

func revokeInvitationUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := global.RevokeInvitation(c.Context(), d.DB, id); err != nil {
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
		if newRole != "owner" && newRole != "admin" && newRole != "member" && newRole != "viewer" {
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
