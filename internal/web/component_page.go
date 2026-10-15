package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// componentPage is GET /projects/:id/components/:cid. A component's page
// is its metrics, one tab per kind: it opens on the first kind it has
// bound, in bindableKinds order — SonarQube when there is one — by a
// redirect (302, not 301: which kind that is changes as bindings do).
// Only a component with nothing bound is drawn here: its header, the
// strip of unbound tabs, and the way to bind one.
func componentPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, comp, err := resolveComponentRoute(c)
		if err != nil {
			return err
		}
		bindings, err := repo.ListBindingsForComponent(c.Context(), s, comp.ID, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if kind := firstBoundKind(bindings); kind != "" {
			return c.Redirect(metricURL(project, comp, kind), fiber.StatusFound)
		}

		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}

		menus, err := loadTrailMenus(c.Context(), s, projects, project, comp, "")
		if err != nil {
			return writeRepoErr(c, err)
		}

		c.Type("html")
		return renderPage(c, d, "pages/component.html", httpx.ViewData{
			Title: projectTitle(project, comp.Name), Org: org, User: user, Nav: navWithTrail("projects", len(projects), componentTrail(folders, project, comp, menus)),
			Data: struct {
				Component  repo.Component
				Project    repo.Project
				URL        string // the component's own page; its links hang off it
				ProjectURL string
				// Tabs is the component's metric strip, every kind unbound.
				Tabs []metricTab
			}{Component: *comp, Project: *project, URL: componentURL(project, comp), ProjectURL: projectURL(project),
				Tabs: componentTabs(project, comp, nil, nil)},
		})
	}
}

func setBindingEnabledUI(enabled bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		binding, err := repo.BindingByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := repo.SetBindingEnabled(c.Context(), s, id, enabled); err != nil {
			return writeRepoErr(c, err)
		}
		return redirectToBindingComponent(c, s, binding, binding.Kind)
	}
}

func deleteBindingUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		binding, err := repo.BindingByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := repo.SoftDeleteBinding(c.Context(), s, id); err != nil {
			return writeRepoErr(c, err)
		}
		// The kind is gone: back to the component, which opens on whatever
		// it still has bound.
		return redirectToBindingComponent(c, s, binding, "")
	}
}

// redirectToBindingComponent ends a binding action on its component's
// metric page for kind — the page the action was taken from — or, with
// kind "", on the component's page.
func redirectToBindingComponent(c *fiber.Ctx, s tenant.Scope, binding *repo.Binding, kind string) error {
	comp, err := repo.ComponentByID(c.Context(), s, binding.ComponentID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	project, err := repo.ProjectByID(c.Context(), s, comp.ProjectID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	if kind != "" {
		return c.Redirect(metricURL(project, comp, kind), fiber.StatusFound)
	}
	return c.Redirect(componentURL(project, comp), fiber.StatusFound)
}

// testBindingUI is an htmx endpoint (task 4.3): POST returns a small HTML
// fragment with the result, swapped into a placeholder next to the "Test"
// button — the same underlying call as bindingHandlers.test (JSON API),
// just rendered for a human instead of a script. Never persists anything;
// Test Binding is explicitly a live, ephemeral check.
func testBindingUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		binding, err := repo.BindingByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		conn, err := repo.ConnectionByID(c.Context(), s, binding.ConnectionID)
		if err != nil {
			return writeRepoErr(c, err)
		}

		dek, err := global.GetOrCreateOrgDEK(c.Context(), d.DB, d.KEK, org.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		secret, err := repo.DecryptConnectionSecret(c.Context(), s, dek, conn.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		client := netguard.NewClient(netguard.Config{AllowHTTP: d.Cfg.Dev, Resolver: d.NetguardResolver, IsBlocked: d.NetguardIsBlocked})
		result, testErr := integrations.TestBinding(c.Context(), client, binding.Kind, conn.BaseURL, secret, binding.ExternalKey)
		secret = "" //nolint:ineffassign // best-effort scrub once no longer needed

		c.Type("html")
		return d.Renderer.RenderPartial(c, "partials/binding_test_result.html", struct {
			OK       bool
			AuthFail bool
			Summary  string
			ReqErr   string
		}{
			OK:       testErr == nil && result.OK,
			AuthFail: testErr == nil && result.AuthFailed,
			Summary:  result.Summary,
			ReqErr:   errString(testErr),
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// registerComponentPageRoutes runs after /projects/:id/components/new is
// registered, so that fixed route wins over a component slug "new".
func registerComponentPageRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/projects/:id/components/:cid", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), componentPage(d))
	// The component page's old address, still in bookmarks.
	app.Get("/components/:component_id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), legacyComponentRedirect(""))
	app.Post("/bindings/:id/enable-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), setBindingEnabledUI(true))
	app.Post("/bindings/:id/disable-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), setBindingEnabledUI(false))
	app.Post("/bindings/:id/delete-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), deleteBindingUI())
	app.Post("/bindings/:id/test-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), testBindingUI(d))
}
