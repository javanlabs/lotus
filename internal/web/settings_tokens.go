package web

import (
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// tokenFlash is a process-local, one-read store for a just-issued token's
// plaintext. A plain POST response that renders the plaintext directly
// (no redirect) leaves the browser sitting on that POST URL — hitting
// reload there resubmits the form and silently issues a *second* token.
// Redirecting to GET after create (the standard POST-redirect-GET
// pattern) avoids that, but the plaintext then can't ride in the query
// string (it would land in server access logs and browser history) or a
// cookie (same problem, plus it'd outlive the response). Instead the
// redirect carries only an opaque, single-use id; the plaintext lives
// here just long enough for the very next GET to read and delete it, so
// even a refresh of that page shows nothing the second time.
var (
	tokenFlashMu sync.Mutex
	tokenFlashes = map[string]struct {
		plain, label string
		expiresAt    time.Time
	}{}
)

func storeTokenFlash(plain, label string) string {
	id := uuid.NewString()
	tokenFlashMu.Lock()
	defer tokenFlashMu.Unlock()
	for k, v := range tokenFlashes {
		if time.Now().After(v.expiresAt) {
			delete(tokenFlashes, k)
		}
	}
	tokenFlashes[id] = struct {
		plain, label string
		expiresAt    time.Time
	}{plain, label, time.Now().Add(2 * time.Minute)}
	return id
}

func takeTokenFlash(id string) (plain, label string, ok bool) {
	if id == "" {
		return "", "", false
	}
	tokenFlashMu.Lock()
	defer tokenFlashMu.Unlock()
	v, found := tokenFlashes[id]
	if !found || time.Now().After(v.expiresAt) {
		return "", "", false
	}
	delete(tokenFlashes, id)
	return v.plain, v.label, true
}

type tokenRow struct {
	ID          string
	Name        string
	TokenPrefix string
	CreatedBy   string
	LastUsedAt  string
	ExpiresAt   string
}

func buildTokenRow(t global.APIToken) tokenRow {
	row := tokenRow{ID: t.ID.String(), Name: t.Name, TokenPrefix: t.TokenPrefix, LastUsedAt: "never", ExpiresAt: "never"}
	if t.LastUsedAt != nil {
		row.LastUsedAt = relativeTimeStr(*t.LastUsedAt)
	}
	if t.ExpiresAt != nil {
		row.ExpiresAt = t.ExpiresAt.Local().Format("2006-01-02")
	}
	return row
}

func settingsTokensPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		plain, label, _ := takeTokenFlash(c.Query("created"))
		return renderTokensPage(c, d, plain, label, "", fiber.StatusOK)
	}
}

// renderTokensPage is the GET /settings/tokens render, parameterized by an
// optional freshly-created token's plaintext (plain/label, read at most
// once via takeTokenFlash — see tokenFlash's own doc comment) or a
// validation error (errMsg) from a failed create — task 4.5's "Plaintext
// tampil tepat sekali" requirement.
func renderTokensPage(c *fiber.Ctx, d Deps, plain, label, errMsg string, status int) error {
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
	pats, err := global.ListAPITokensForUser(c.Context(), d.DB, chrome.Principal.UserID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	patRows := make([]tokenRow, 0, len(pats))
	for _, t := range pats {
		patRows = append(patRows, buildTokenRow(t))
	}
	var serviceRows []tokenRow
	canManageOrg := chrome.Principal.Can("token.manage.org")
	if canManageOrg {
		services, err := global.ListAPITokensForOrg(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, t := range services {
			serviceRows = append(serviceRows, buildTokenRow(t))
		}
	}
	nav := buildNav("", len(projects))
	nav.Crumb = "Settings"
	c.Type("html")
	c.Status(status)
	return d.Renderer.Render(c, "pages/settings_tokens.html", httpx.ViewData{
		Title: "Tokens", Org: org, User: user, Nav: nav,
		Data: struct {
			SettingsNav   []settingsNavItem
			PATs          []tokenRow
			ServiceTokens []tokenRow
			CanManageOrg  bool
			NewTokenPlain string
			NewTokenLabel string
			Error         string
		}{SettingsNav: buildSettingsNav("tokens"), PATs: patRows, ServiceTokens: serviceRows, CanManageOrg: canManageOrg,
			NewTokenPlain: plain, NewTokenLabel: label, Error: errMsg},
	})
}

func createPATUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(c.FormValue("name"))
		if name == "" {
			return renderTokensPage(c, d, "", "", "a token name is required", fiber.StatusUnprocessableEntity)
		}
		raw, err := authn.IssuePersonalAccessToken(c.Context(), authn.Deps{DB: d.DB}, authn.NewPersonalAccessTokenParams{
			UserID: chrome.Principal.UserID, Name: name, Scopes: authz.PermissionsForRole(chrome.Principal.Role),
		})
		if err != nil {
			return writeRepoErr(c, err)
		}
		id := storeTokenFlash(raw, name)
		return c.Redirect("/settings/tokens?created="+id, fiber.StatusSeeOther)
	}
}

func createServiceTokenUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(c.FormValue("name"))
		if name == "" {
			return renderTokensPage(c, d, "", "", "a token name is required", fiber.StatusUnprocessableEntity)
		}
		raw, err := authn.IssueServiceToken(c.Context(), authn.Deps{DB: d.DB}, authn.NewServiceTokenParams{
			OrgID: chrome.OrgID, ActorID: chrome.Principal.UserID, ActorRole: chrome.Principal.Role, Name: name,
			Scopes: authz.PermissionsForRole(chrome.Principal.Role),
		})
		if err != nil {
			return writeRepoErr(c, err)
		}
		id := storeTokenFlash(raw, name)
		return c.Redirect("/settings/tokens?created="+id, fiber.StatusSeeOther)
	}
}

func revokeTokenUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := global.RevokeAPIToken(c.Context(), d.DB, id); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect("/settings/tokens", fiber.StatusFound)
	}
}

func registerTokenSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/settings/tokens", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("token.manage.own"), settingsTokensPage(d))
	app.Post("/settings/tokens/pat", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("token.manage.own"), createPATUI(d))
	app.Post("/settings/tokens/service", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("token.manage.org"), createServiceTokenUI(d))
	app.Post("/settings/tokens/:id/revoke", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("token.manage.own"), revokeTokenUI(d))
}
