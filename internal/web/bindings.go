package web

import (
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// bindingHandlers, like connectionHandlers, closes over Deps — the Test
// Binding action needs to reach the binding's own Connection's secret,
// which means d.DB (for the org's DEK) and d.KEK.
type bindingHandlers struct {
	d Deps
}

type createBindingRequest struct {
	ConnectionID uuid.UUID       `json:"connection_id"`
	Kind         string          `json:"kind"`
	ExternalKey  string          `json:"external_key"`
	Config       json.RawMessage `json:"config"`
}

func (h bindingHandlers) create(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	componentID, err := uuid.Parse(c.Params("component_id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req createBindingRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.ConnectionID == uuid.Nil || req.Kind == "" || req.ExternalKey == "" {
		return fiber.NewError(fiber.StatusBadRequest, "connection_id, kind, and external_key are required")
	}
	binding, err := repo.CreateBinding(c.Context(), s, repo.NewBinding{
		ComponentID: componentID, ConnectionID: req.ConnectionID, Kind: req.Kind,
		ExternalKey: req.ExternalKey, Config: req.Config,
	})
	if err != nil {
		return writeBindingErr(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(binding)
}

func (h bindingHandlers) list(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	componentID, err := uuid.Parse(c.Params("component_id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	includeDeleted := c.Query("include_deleted") == "true"
	bindings, err := repo.ListBindingsForComponent(c.Context(), s, componentID, includeDeleted)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(bindings)
}

func (h bindingHandlers) get(c *fiber.Ctx) error {
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
	return c.JSON(binding)
}

type updateBindingConfigRequest struct {
	Config json.RawMessage `json:"config"`
}

func (h bindingHandlers) updateConfig(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req updateBindingConfigRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	binding, err := repo.UpdateBindingConfig(c.Context(), s, id, req.Config)
	if err != nil {
		return writeBindingErr(c, err)
	}
	return c.JSON(binding)
}

func (h bindingHandlers) setEnabled(enabled bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.SetBindingEnabled(c.Context(), s, id, enabled); err != nil {
			return writeRepoErr(c, err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	}
}

func (h bindingHandlers) delete(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	if err := repo.SoftDeleteBinding(c.Context(), s, id); err != nil {
		return writeRepoErr(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// test is task 2.3's "Test binding": call upstream once, using the
// binding's own connection's credential, and report what's found — the
// same write-only discipline as task 2.2's Test Connection applies here
// too, the decrypted secret never leaves this function.
func (h bindingHandlers) test(c *fiber.Ctx) error {
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

	dek, err := global.GetOrCreateOrgDEK(c.Context(), h.d.DB, h.d.KEK, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	secret, err := repo.DecryptConnectionSecret(c.Context(), s, dek, conn.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	client := netguard.NewClient(netguard.Config{AllowHTTP: h.d.Cfg.Dev, Resolver: h.d.NetguardResolver, IsBlocked: h.d.NetguardIsBlocked})
	result, testErr := integrations.TestBinding(c.Context(), client, binding.Kind, conn.BaseURL, secret, binding.ExternalKey)
	secret = "" //nolint:ineffassign // best-effort scrub once no longer needed

	if testErr != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": testErr.Error()})
	}
	return c.JSON(fiber.Map{"ok": result.OK, "auth_failed": result.AuthFailed, "summary": result.Summary})
}

// writeBindingErr maps CreateBinding/UpdateBindingConfig's error results.
// Anything that isn't one of repo's own sentinels is a Descriptor
// ValidateConfig failure (e.g. "service_tag_value is required when mode
// is \"shared\"") — already a human-readable message, surfaced as-is.
func writeBindingErr(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, repo.ErrBindingTargetTaken):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, repo.ErrNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
	case errors.Is(err, repo.ErrInvalidInput):
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid input"})
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
}

func registerBindingRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	h := bindingHandlers{d: d}
	app.Post("/components/:component_id/bindings", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.create)
	app.Get("/components/:component_id/bindings", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), h.list)
	app.Get("/bindings/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), h.get)
	app.Patch("/bindings/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.updateConfig)
	app.Post("/bindings/:id/enable", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.setEnabled(true))
	app.Post("/bindings/:id/disable", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), h.setEnabled(false))
	app.Delete("/bindings/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.unbind"), h.delete)
	app.Post("/bindings/:id/test", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.test)
}
