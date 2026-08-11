package authn

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// signupSlugFormat is task 1.6's own (tighter than the general resolver's)
// slug rule: 3-40 characters, lowercase alphanumeric with internal
// hyphens, never leading/trailing. The general DB CHECK (task 0.2) allows
// the fuller 1-63 char range since it has to accept whatever already
// exists; this is the narrower, more opinionated range the signup form
// itself imposes as a product decision.
var signupSlugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// Signup renders the two OAuth buttons — GET /signup on the auth host.
// There is deliberately no separate "log in" page: GitHub/Google login and
// signup are the same OAuth flow, and ResolveUser (task 1.1) already
// recognizes a returning identity.
func Signup(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return renderLoginPicker(c, deps.Renderer, deps.OAuthProviders, "/welcome")
	}
}

// Welcome is GET /welcome: display-name capture, prefilled from the OAuth
// identity, skippable (an empty submit just keeps whatever CreateUser
// already set — including "").
func Welcome(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		session, _ := CurrentSession(c)
		user, err := global.UserByID(c.Context(), deps.DB, session.UserID)
		if err != nil {
			return err
		}
		c.Type("html")
		return deps.Renderer.Render(c, "pages/welcome.html", httpx.ViewData{
			Title: "Welcome",
			Data:  struct{ Name string }{user.Name},
		})
	}
}

// SubmitWelcome is POST /welcome.
func SubmitWelcome(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		session, _ := CurrentSession(c)
		name := strings.TrimSpace(c.FormValue("name"))
		if name != "" {
			if err := global.UpdateUserName(c.Context(), deps.DB, session.UserID, name); err != nil {
				return err
			}
		}
		return c.Redirect("/onboarding/org", fiber.StatusFound)
	}
}

// onboardingOrgFormData is the shape pages/onboarding_org.html always
// receives, whether from the initial GET (Error == "") or a failed POST
// (Name/Slug preserved so the user doesn't retype them, Error explaining
// why).
type onboardingOrgFormData struct {
	Name, Slug, Error string
}

// OnboardingOrg is GET /onboarding/org.
func OnboardingOrg(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Type("html")
		return deps.Renderer.Render(c, "pages/onboarding_org.html", httpx.ViewData{
			Title: "Create your organization",
			Data:  onboardingOrgFormData{},
		})
	}
}

// SlugCheckResult is what the htmx slug-availability fragment renders.
type SlugCheckResult struct {
	Slug      string
	Available bool
	Reason    string // shown when not available
}

// SlugCheck is GET /onboarding/slug-check?slug= — task 1.6's debounced
// htmx availability check. Advisory only: SubmitOnboardingOrg's INSERT is
// what actually decides, via the subdomain UNIQUE constraint, so a race
// between this check and the real submit can never leave two orgs with
// the same slug, only an occasional stale "available" a moment before
// someone else wins the insert.
func SlugCheck(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		slug := strings.ToLower(strings.TrimSpace(c.Query("slug")))
		result := SlugCheckResult{Slug: slug}

		if err := ValidateSignupSlugFormat(slug); err != nil {
			result.Reason = err.Error()
		} else if reserved, err := global.IsReservedSubdomain(c.Context(), deps.DB, slug); err != nil {
			return err
		} else if reserved {
			result.Reason = "that name is reserved"
		} else if retired, err := global.IsRetiredSlug(c.Context(), deps.DB, slug); err != nil {
			return err
		} else if retired {
			result.Reason = "that name was used before and can't be reused"
		} else if _, err := global.OrgBySubdomain(c.Context(), deps.DB, slug); err == nil {
			result.Reason = "already taken"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else {
			result.Available = true
		}

		c.Type("html")
		return deps.Renderer.RenderPartial(c, "partials/slug_check.html", result)
	}
}

// SubmitOnboardingOrg is POST /onboarding/org: creates the organization
// with the current user as Owner, then moves on to the invite screen.
func SubmitOnboardingOrg(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		session, _ := CurrentSession(c)
		name := strings.TrimSpace(c.FormValue("name"))
		slug := strings.ToLower(strings.TrimSpace(c.FormValue("slug")))

		if err := ValidateSignupSlugFormat(slug); err != nil {
			return renderOnboardingOrgError(c, deps.Renderer, name, slug, err.Error())
		}
		if reserved, err := global.IsReservedSubdomain(c.Context(), deps.DB, slug); err != nil {
			return err
		} else if reserved {
			return renderOnboardingOrgError(c, deps.Renderer, name, slug, "that name is reserved")
		}
		if retired, err := global.IsRetiredSlug(c.Context(), deps.DB, slug); err != nil {
			return err
		} else if retired {
			return renderOnboardingOrgError(c, deps.Renderer, name, slug, "that name was used before and can't be reused")
		}

		if _, err := global.CreateOrgWithOwner(c.Context(), deps.DB, slug, name, session.UserID); err != nil {
			if errors.Is(err, global.ErrSlugTaken) {
				return renderOnboardingOrgError(c, deps.Renderer, name, slug, "already taken — try another")
			}
			return err
		}

		return c.Redirect("/onboarding/invite?org="+slug, fiber.StatusFound)
	}
}

func renderOnboardingOrgError(c *fiber.Ctx, renderer *httpx.Renderer, name, slug, reason string) error {
	c.Type("html")
	c.Status(fiber.StatusUnprocessableEntity)
	return renderer.Render(c, "pages/onboarding_org.html", httpx.ViewData{
		Title: "Create your organization",
		Data:  struct{ Name, Slug, Error string }{name, slug, reason},
	})
}

// OnboardingInvite is GET /onboarding/invite?org=<slug>.
func OnboardingInvite(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		org, err := requireOwnedOrg(c, deps)
		if err != nil {
			return err
		}
		c.Type("html")
		return deps.Renderer.Render(c, "pages/onboarding_invite.html", httpx.ViewData{
			Title: "Invite your team",
			Data:  struct{ OrgSlug string }{org.Subdomain},
		})
	}
}

// SubmitOnboardingInvite is POST /onboarding/invite: creates a pending
// invitation per non-empty email (skippable — an empty list is a normal,
// first-class path, not an error), then hands off to the new tenant via
// the same mechanism every other login uses (task 1.2) — no special-cased
// "just-created-it" shortcut, so the happy path after signup exercises the
// exact code every other login does.
func SubmitOnboardingInvite(deps Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		session, _ := CurrentSession(c)
		org, err := requireOwnedOrg(c, deps)
		if err != nil {
			return err
		}

		roleKey := c.FormValue("role")
		if roleKey == "" {
			roleKey = "member"
		}
		var emails []string
		if c.FormValue("action") != "skip" {
			emails = parseEmailList(c.FormValue("emails"))
		}
		for _, email := range emails {
			_, err := SendInvitation(c.Context(), deps, SendInvitationParams{
				OrgID:     org.ID,
				OrgName:   org.Name,
				ActorID:   session.UserID,
				ActorRole: "owner", // requireOwnedOrg only lets the creator (Owner) reach this handler
				Email:     email,
				RoleKey:   roleKey,
			})
			switch {
			case errors.Is(err, global.ErrInvitationPending), errors.Is(err, ErrInviteRateLimited):
				// Not fatal to the onboarding flow — the org still gets
				// created and the owner still lands on their dashboard.
			case err != nil:
				return err
			}
		}

		dest := deps.Cfg.Scheme() + "://" + org.Subdomain + "." + deps.Cfg.BaseDomain + "/handoff/start?return=/"
		return c.Redirect(dest, fiber.StatusFound)
	}
}

// requireOwnedOrg resolves the ?org= query param and verifies the current
// global-session user actually has invite rights on it — without this,
// a signed-in user could pass any org's slug in the query string and
// spam invitations into an organization they don't belong to.
func requireOwnedOrg(c *fiber.Ctx, deps Deps) (*global.Org, error) {
	session, ok := CurrentSession(c)
	if !ok {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "no session")
	}
	slug := c.Query("org")
	org, err := global.OrgBySubdomain(c.Context(), deps.DB, slug)
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	membership, err := global.ActiveMembership(c.Context(), deps.DB, session.UserID, org.ID)
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	principal := authz.Principal{Kind: authz.KindSession, Role: membership.RoleKey}
	if !principal.Can("member.invite") {
		return nil, fiber.NewError(fiber.StatusForbidden, "missing permission: member.invite")
	}
	return org, nil
}

func parseEmailList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		e := strings.ToLower(strings.TrimSpace(f))
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// ValidateSignupSlugFormat is exported so internal/web's own subdomain-
// change flow (task 4.5's /settings/organization danger zone) can reuse
// the exact same format rule signup already enforces, rather than a
// second copy of it drifting out of sync.
func ValidateSignupSlugFormat(slug string) error {
	if !signupSlugFormat.MatchString(slug) {
		return errors.New("must be 3-40 characters: lowercase letters, numbers, and hyphens, not starting or ending with a hyphen")
	}
	if strings.Contains(slug, "--") {
		return errors.New("can't contain two hyphens in a row")
	}
	if !tenant.ValidSubdomainLabel(slug) {
		return errors.New("not a valid subdomain")
	}
	return nil
}

// slugify turns an organization name into a candidate slug: lowercase,
// non-alphanumerics collapsed to single hyphens, trimmed of leading/
// trailing hyphens, capped to the signup form's 40-char limit. Purely a
// UI convenience — the availability check and the real INSERT are what
// actually validate it, per task 1.6.
func slugify(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
