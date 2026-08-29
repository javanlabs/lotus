package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

func settingsOrgPage(d Deps) fiber.Handler {
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
		profile, err := global.OrgProfileByID(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		members, err := global.ListMembersForOrg(c.Context(), d.DB, chrome.OrgID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		// Built here rather than in the template: the dropdown takes a
		// flat option list, and a template cannot accumulate a slice
		// inside a range.
		transferTargets := make([]httpx.SelectOption, 0, len(members))
		for _, m := range members {
			if m.UserID != chrome.Principal.UserID {
				transferTargets = append(transferTargets, httpx.SelectOption{
					Value: m.UserID.String(),
					Label: fmt.Sprintf("%s (%s)", m.Name, m.Email),
				})
			}
		}

		defaultInterval := ""
		if profile.DefaultSyncIntervalSeconds != nil {
			defaultInterval = strconv.Itoa(*profile.DefaultSyncIntervalSeconds / 60)
		}

		nav := buildNav("", len(projects))
		nav.Trail = []httpx.NavCrumb{{Label: "Settings"}}
		c.Type("html")
		return d.Renderer.Render(c, "pages/settings_organization.html", httpx.ViewData{
			Title: "Organization", Org: org, User: user, Nav: nav,
			Data: struct {
				SettingsNav            []settingsNavItem
				Profile                *global.OrgProfile
				DefaultIntervalMinutes string
				TransferTargets        []httpx.SelectOption
				CanUpdate              bool
				CanChangeSlug          bool
				CanTransfer            bool
				CanDelete              bool
				IsOwner                bool
				Error                  string
			}{
				SettingsNav: buildSettingsNav("organization"), Profile: profile, DefaultIntervalMinutes: defaultInterval,
				TransferTargets: transferTargets,
				CanUpdate:       chrome.Principal.Can("org.settings.update"),
				CanChangeSlug:   chrome.Principal.Can("org.slug.change"),
				CanTransfer:     chrome.Principal.Can("org.transfer_ownership"),
				CanDelete:       chrome.Principal.Can("org.delete"),
				IsOwner:         chrome.Principal.Role == "owner",
				Error:           c.Query("error"),
			},
		})
	}
}

// redirectWithOrgError sends the browser back to the settings page with a
// message displayed inline, instead of the bare {"error":...} JSON a plain
// fiber.NewError would otherwise produce for a form POST — every other
// settings/onboarding form in this app re-renders its own page on a
// validation failure (see renderOnboardingOrgError, renderTokensPage);
// this is that same pattern's equivalent for a plain (non-htmx) POST that
// doesn't already have a render-with-flash helper of its own.
func redirectWithOrgError(c *fiber.Ctx, msg string) error {
	return c.Redirect("/settings/organization?error="+url.QueryEscape(msg), fiber.StatusSeeOther)
}

func updateOrgProfileUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(c.FormValue("name"))
		timezone := strings.TrimSpace(c.FormValue("timezone"))
		if name == "" || timezone == "" {
			return redirectWithOrgError(c, "name and timezone are required")
		}
		retentionDays, err := strconv.Atoi(c.FormValue("retention_days", "90"))
		if err != nil || retentionDays < 1 {
			return redirectWithOrgError(c, "retention_days must be a positive number")
		}
		var defaultInterval *int
		if raw := strings.TrimSpace(c.FormValue("default_sync_interval_minutes")); raw != "" {
			minutes, err := strconv.Atoi(raw)
			if err != nil || minutes < 1 {
				return redirectWithOrgError(c, "default sync interval must be a positive number of minutes")
			}
			seconds := minutes * 60
			defaultInterval = &seconds
		}
		if err := global.UpdateOrgProfile(c.Context(), d.DB, chrome.OrgID, name, timezone, defaultInterval, retentionDays); err != nil {
			return writeRepoErr(c, err)
		}
		meta, _ := json.Marshal(fiber.Map{"name": name, "timezone": timezone, "retention_days": retentionDays})
		if err := recordOrgAuditEvent(c, chrome, "org.settings_updated", meta); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect("/settings/organization", fiber.StatusFound)
	}
}

func updateOrgSubdomainUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		newSlug := strings.ToLower(strings.TrimSpace(c.FormValue("subdomain")))
		if err := authn.ValidateSignupSlugFormat(newSlug); err != nil {
			return redirectWithOrgError(c, err.Error())
		}
		if reserved, err := global.IsReservedSubdomain(c.Context(), d.DB, newSlug); err != nil {
			return writeRepoErr(c, err)
		} else if reserved {
			return redirectWithOrgError(c, "that name is reserved")
		}
		if retired, err := global.IsRetiredSlug(c.Context(), d.DB, newSlug); err != nil {
			return writeRepoErr(c, err)
		} else if retired {
			return redirectWithOrgError(c, "that name was used before and can't be reused")
		}
		if err := global.UpdateOrgSubdomain(c.Context(), d.DB, chrome.OrgID, newSlug); err != nil {
			if err == global.ErrSlugTaken {
				return redirectWithOrgError(c, "already taken — try another")
			}
			return writeRepoErr(c, err)
		}
		meta, _ := json.Marshal(fiber.Map{"new_subdomain": newSlug})
		if err := recordOrgAuditEvent(c, chrome, "org.subdomain_changed", meta); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect("https://"+newSlug+"."+d.Cfg.BaseDomain+"/settings/organization", fiber.StatusFound)
	}
}

func transferOwnershipUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		targetID, err := uuid.Parse(c.FormValue("user_id"))
		if err != nil {
			return redirectWithOrgError(c, "choose a member to transfer to")
		}
		if err := global.TransferOwnership(c.Context(), d.DB, chrome.OrgID, chrome.Principal.UserID, targetID); err != nil {
			return writeRepoErr(c, err)
		}
		meta, _ := json.Marshal(fiber.Map{"new_owner": targetID})
		if err := recordOrgAuditEvent(c, chrome, "org.ownership_transferred", meta); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect("/settings/organization", fiber.StatusFound)
	}
}

func deleteOrgUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		chrome, err := loadSettingsChrome(c)
		if err != nil {
			return err
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		confirm := strings.TrimSpace(c.FormValue("confirm_subdomain"))
		if confirm != org.Subdomain {
			return redirectWithOrgError(c, "type the organization's subdomain exactly to confirm deletion")
		}
		if err := recordOrgAuditEvent(c, chrome, "org.deleted", nil); err != nil {
			return writeRepoErr(c, err)
		}
		if err := global.SoftDeleteOrganization(c.Context(), d.DB, chrome.OrgID); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect("https://"+d.Cfg.BaseDomain, fiber.StatusFound)
	}
}

func recordOrgAuditEvent(c *fiber.Ctx, chrome *settingsChrome, action string, metadata []byte) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	var actor *uuid.UUID
	if chrome.Principal.UserID != uuid.Nil {
		actor = &chrome.Principal.UserID
	}
	return repo.RecordAuditEvent(c.Context(), s, actor, action, "organization", &chrome.OrgID, metadata)
}

func registerOrgSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/settings/organization", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("org.settings.update"), settingsOrgPage(d))
	app.Post("/settings/organization", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("org.settings.update"), updateOrgProfileUI(d))
	app.Post("/settings/organization/subdomain", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("org.slug.change"), updateOrgSubdomainUI(d))
	app.Post("/settings/organization/transfer-ownership", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("org.transfer_ownership"), transferOwnershipUI(d))
	app.Post("/settings/organization/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("org.delete"), deleteOrgUI(d))
}
