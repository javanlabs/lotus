// Package web's settings_*.go files are task 4.5: the organization
// administration surface — Connections, Members, Organization, Tokens,
// Ingest docs, and the org-wide Activity feed. SSO (/settings/sso) is
// deliberately not part of this task: it's a materially different,
// security-critical body of work (dynamic per-org OIDC, DNS domain
// verification, break-glass enforcement) that was scoped out for its own
// follow-up pass rather than folded in here unreviewed.
package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/tenant"
)

// settingsNavItem is one tab of the shared settings sub-nav — see
// web/templates/components/settings_nav.html.
type settingsNavItem struct {
	Key, Label, Href string
	Active           bool
}

func buildSettingsNav(active string) []settingsNavItem {
	items := []settingsNavItem{
		{Key: "organization", Label: "Organization", Href: "/settings/organization"},
		{Key: "connections", Label: "Connections", Href: "/settings/connections"},
		{Key: "members", Label: "Members", Href: "/settings/members"},
		{Key: "tokens", Label: "Tokens", Href: "/settings/tokens"},
		{Key: "ingest", Label: "Ingest", Href: "/settings/ingest"},
	}
	for i := range items {
		items[i].Active = items[i].Key == active
	}
	return items
}

// settingsChrome is what every settings page handler needs beyond the
// usual shellChrome trio: the resolved org id (settings pages act on the
// organization itself, not a project/component/binding chain) and the
// current principal, since several actions (role changes, ownership
// transfer, deletes) need to know who's making the request for the audit
// row, not just whether authz.Require already let them through.
type settingsChrome struct {
	OrgID     uuid.UUID
	Principal authz.Principal
}

func loadSettingsChrome(c *fiber.Ctx) (*settingsChrome, error) {
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return nil, fiber.ErrNotFound
	}
	p, ok := authz.CurrentPrincipal(c)
	if !ok {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "no principal")
	}
	return &settingsChrome{OrgID: org.ID, Principal: p}, nil
}

func registerSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	registerConnectionSettingsRoutes(app, d, requireSession, loadPrincipal)
	registerMemberSettingsRoutes(app, d, requireSession, loadPrincipal)
	registerOrgSettingsRoutes(app, d, requireSession, loadPrincipal)
	registerTokenSettingsRoutes(app, d, requireSession, loadPrincipal)
	registerIngestSettingsRoutes(app, d, requireSession, loadPrincipal)
	registerActivityRoutes(app, d, requireSession, loadPrincipal)
}
