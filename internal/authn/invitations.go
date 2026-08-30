package authn

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo/global"
)

const (
	invitationTTL = 7 * 24 * time.Hour

	// Rate limits (task 1.7): invite spam is a real abuse vector against
	// the sending domain's reputation, not a hypothetical to defer.
	inviteRateLimitOrgWindow   = time.Hour
	inviteRateLimitOrgMax      = 20
	inviteRateLimitEmailWindow = 24 * time.Hour
	inviteRateLimitEmailMax    = 3
)

// ErrCannotInviteOwner is the escalation guard: only an Owner can invite
// someone as Owner. Admin has every other invite permission but this one —
// the same asymmetry authz.CanChangeRole enforces for role changes on
// existing members.
var ErrCannotInviteOwner = errors.New("only an Owner can invite someone as Owner")

// ErrInviteRateLimited means the org or destination email has hit task
// 1.7's sending cap.
var ErrInviteRateLimited = errors.New("too many invitations sent recently")

// SendInvitationParams is the input to SendInvitation.
type SendInvitationParams struct {
	OrgID     uuid.UUID
	OrgName   string
	ActorID   uuid.UUID
	ActorRole string // the inviter's current role, for the escalation guard
	Email     string
	RoleKey   string
}

// SendInvitation creates an invitation (enforcing the escalation guard and
// rate limits), and — if SMTP is configured — emails the link. Returns the
// invite link either way, since a copyable link (task 1.6) works whether
// or not mail delivery does.
func SendInvitation(ctx context.Context, deps Deps, p SendInvitationParams) (string, error) {
	if p.RoleKey == "owner" && p.ActorRole != "owner" {
		return "", ErrCannotInviteOwner
	}

	if n, err := global.CountRecentInvitationsByOrg(ctx, deps.DB, p.OrgID, time.Now().Add(-inviteRateLimitOrgWindow)); err != nil {
		return "", err
	} else if n >= inviteRateLimitOrgMax {
		return "", ErrInviteRateLimited
	}
	if n, err := global.CountRecentInvitationsByEmail(ctx, deps.DB, p.Email, time.Now().Add(-inviteRateLimitEmailWindow)); err != nil {
		return "", err
	} else if n >= inviteRateLimitEmailMax {
		return "", ErrInviteRateLimited
	}

	raw := randToken(32)
	_, err := global.CreateInvitation(ctx, deps.DB, global.NewInvitation{
		OrgID:     p.OrgID,
		Email:     p.Email,
		RoleKey:   p.RoleKey,
		InvitedBy: p.ActorID,
		TokenHash: sha256Hex(raw),
		ExpiresAt: time.Now().Add(invitationTTL),
	})
	if err != nil {
		return "", err
	}

	link := deps.Cfg.BaseURL() + "/invite/" + raw
	if deps.Mailer.Enabled() {
		_ = deps.Mailer.Send(mailer.Message{
			To:      mailer.Address{Email: p.Email},
			Subject: "You're invited to " + p.OrgName + " on Lotus",
			HTML:    inviteEmailHTML(p.OrgName, link),
		})
	}
	return link, nil
}

func inviteEmailHTML(orgName, link string) string {
	return "<p>You've been invited to join <strong>" + orgName + "</strong> on Lotus.</p>" +
		"<p><a href=\"" + link + "\">Accept the invitation</a></p>" +
		"<p>This link expires in 7 days.</p>"
}

// inviteViewState is what pages/invite.html renders for each of task 1.7's
// accept-flow branches.
type inviteViewState struct {
	OrgName     string
	InviterName string
	RoleKey     string
	Token       string
	State       string // "login" | "mismatch_soft" | "mismatch_hard" | "invalid"
	ActorEmail  string // set only for the mismatch states, for the warning copy

	// Providers is filled in by renderInvite from the live registry. The
	// template used to hardcode a GitHub and a Google button, which meant
	// an instance configured with neither — this one, which uses OIDC —
	// showed an invitee two buttons that could only fail, and no way in.
	Providers []string
}

// InviteLanding is GET /invite/:token on the auth host — task 1.7's accept
// entry point. Not logged in: show who invited them and to what, with
// login buttons. Logged in with a verified email matching the invite:
// accept immediately. Logged in with a different email: soft warning
// (Member/Viewer, needs one more confirming click) or hard rejection
// (Admin/Owner — an invite link forwarded into a Slack channel must never
// silently hand admin rights to whoever clicks it first).
func InviteLanding(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := c.Params("token")
		inv, err := global.InvitationByTokenHash(c.Context(), deps.DB, sha256Hex(token))
		if err != nil {
			return renderInvite(c, deps, inviteViewState{Token: token, State: "invalid"})
		}
		org, err := global.OrgByID(c.Context(), deps.DB, inv.OrgID)
		if err != nil {
			return renderInvite(c, deps, inviteViewState{Token: token, State: "invalid"})
		}
		inviter, _ := global.UserByID(c.Context(), deps.DB, inv.InvitedBy)
		inviterName := ""
		if inviter != nil {
			inviterName = inviter.Name
		}

		session, ok := currentGlobalSession(c, deps.DB, deps.CookieSecure)
		if !ok {
			return renderInvite(c, deps, inviteViewState{
				OrgName: org.Name, InviterName: inviterName, RoleKey: inv.RoleKey, Token: token, State: "login",
			})
		}

		user, err := global.UserByID(c.Context(), deps.DB, session.UserID)
		if err != nil {
			return err
		}
		if emailMatches(user, inv.Email) {
			if err := global.AcceptInvitation(c.Context(), deps.DB, inv.ID, session.UserID); err != nil {
				return err
			}
			return c.Redirect(deps.Cfg.Scheme()+"://"+org.Subdomain+"."+deps.Cfg.BaseDomain+"/handoff/start?return=/", fiber.StatusFound)
		}

		actorEmail := ""
		if user.Email != nil {
			actorEmail = *user.Email
		}
		state := "mismatch_soft"
		if inv.RoleKey == "admin" || inv.RoleKey == "owner" {
			state = "mismatch_hard"
		}
		return renderInvite(c, deps, inviteViewState{
			OrgName: org.Name, InviterName: inviterName, RoleKey: inv.RoleKey, Token: token,
			State: state, ActorEmail: actorEmail,
		})
	}
}

// InviteConfirm is POST /invite/:token/confirm — the explicit second click
// required for a Member/Viewer invite accepted under a different verified
// email than the invite was sent to. Still hard-rejects Admin/Owner
// invites even if this endpoint is hit directly, defense in depth against
// InviteLanding's own state computation ever having a bug.
func InviteConfirm(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := c.Params("token")
		inv, err := global.InvitationByTokenHash(c.Context(), deps.DB, sha256Hex(token))
		if err != nil {
			return renderInvite(c, deps, inviteViewState{Token: token, State: "invalid"})
		}
		if inv.RoleKey == "admin" || inv.RoleKey == "owner" {
			return renderInvite(c, deps, inviteViewState{Token: token, RoleKey: inv.RoleKey, State: "mismatch_hard"})
		}
		session, ok := currentGlobalSession(c, deps.DB, deps.CookieSecure)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "no session")
		}
		if err := global.AcceptInvitation(c.Context(), deps.DB, inv.ID, session.UserID); err != nil {
			return err
		}
		org, err := global.OrgByID(c.Context(), deps.DB, inv.OrgID)
		if err != nil {
			return err
		}
		return c.Redirect(deps.Cfg.Scheme()+"://"+org.Subdomain+"."+deps.Cfg.BaseDomain+"/handoff/start?return=/", fiber.StatusFound)
	}
}

// InviteDecline is POST /invite/:token/decline. No session required —
// declining shouldn't demand a login the person never wanted in the first
// place.
func InviteDecline(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := c.Params("token")
		inv, err := global.InvitationByTokenHash(c.Context(), deps.DB, sha256Hex(token))
		if err != nil {
			return renderInvite(c, deps, inviteViewState{Token: token, State: "invalid"})
		}
		if err := global.DeclineInvitation(c.Context(), deps.DB, inv.ID); err != nil {
			return err
		}
		return renderInvite(c, deps, inviteViewState{Token: token, State: "declined"})
	}
}

func emailMatches(user *global.User, invitedEmail string) bool {
	return user.Email != nil && user.EmailVerifiedAt != nil &&
		strings.EqualFold(*user.Email, invitedEmail)
}

func renderInvite(c *fiber.Ctx, deps Deps, data inviteViewState) error {
	data.Providers = providerNames(deps.OAuthProviders)
	c.Type("html")
	return deps.Renderer.Render(c, "pages/invite.html", httpx.ViewData{Title: "Invitation", Data: data})
}
