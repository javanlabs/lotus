package web

import (
	"errors"
	"fmt"

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

// bindingRow is one metric binding as the component page shows it —
// resolved connection name/provider alongside the raw repo.Binding, since
// a binding only carries connection_id. LastStatus is pre-dereferenced
// (repo.Binding.LastStatus is *string, nullable) — html/template prints a
// non-nil pointer as its address, not its pointee, so every *T field a
// template touches must be flattened to a plain value here first.
type bindingRow struct {
	repo.Binding
	ConnectionName string
	KindLabel      string
	LastStatus     string
}

func componentPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		comp, err := repo.ComponentByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		project, err := repo.ProjectByID(c.Context(), s, comp.ProjectID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}

		bindings, err := repo.ListBindingsForComponent(c.Context(), s, comp.ID, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		conns, err := repo.ListConnections(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		connNames := make(map[uuid.UUID]string, len(conns))
		for _, cn := range conns {
			connNames[cn.ID] = cn.Name
		}
		rows := make([]bindingRow, 0, len(bindings))
		for _, b := range bindings {
			lastStatus := ""
			if b.LastStatus != nil {
				lastStatus = *b.LastStatus
			}
			rows = append(rows, bindingRow{
				Binding: b, ConnectionName: connNames[b.ConnectionID], KindLabel: kindLabels[b.Kind], LastStatus: lastStatus,
			})
		}

		metric, err := repo.GetComponentMetric(c.Context(), s, comp.ID)
		var current *repo.ComponentMetric
		tone, scoreText := "none", ""
		switch {
		case err == nil:
			current = metric
			tone = toneOf(metric.StatusValue())
			if metric.Score != nil {
				scoreText = fmt.Sprintf("%d", *metric.Score)
			}
		case errors.Is(err, repo.ErrNotFound):
			// never collected yet — current stays nil, template shows "–"
		default:
			return writeRepoErr(c, err)
		}

		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}

		c.Type("html")
		return renderPage(c, d, "pages/component.html", httpx.ViewData{
			Title: comp.Name, Org: org, User: user, Nav: navWithTrail("projects", len(projects), componentTrail(folders, project, comp)),
			Data: struct {
				Component repo.Component
				Project   repo.Project
				Bindings  []bindingRow
				Metric    *repo.ComponentMetric
				Tone      string
				ScoreText string
				Kinds     []string
				KindLabel map[string]string
			}{Component: *comp, Project: *project, Bindings: rows, Metric: current, Tone: tone, ScoreText: scoreText, Kinds: bindableKinds, KindLabel: kindLabels},
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
		return c.Redirect("/components/"+binding.ComponentID.String(), fiber.StatusFound)
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
		return c.Redirect("/components/"+binding.ComponentID.String(), fiber.StatusFound)
	}
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

func registerComponentPageRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/components/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), componentPage(d))
	app.Post("/bindings/:id/enable-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), setBindingEnabledUI(true))
	app.Post("/bindings/:id/disable-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), setBindingEnabledUI(false))
	app.Post("/bindings/:id/delete-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), deleteBindingUI())
	app.Post("/bindings/:id/test-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), testBindingUI(d))
}
