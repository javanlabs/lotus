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

// connectionHandlers closes over Deps — unlike projects.go's handlers,
// these need d.DB (to fetch/create the org's DEK, a control-plane
// operation tenant.Scope can't reach) and d.KEK and d.Cfg.Dev (for the
// SSRF guard's http-vs-https rule) on every request.
type connectionHandlers struct {
	d Deps
}

func (h connectionHandlers) netguardConfig() netguard.Config {
	return netguard.Config{AllowHTTP: h.d.Cfg.Dev, Resolver: h.d.NetguardResolver, IsBlocked: h.d.NetguardIsBlocked}
}

func (h connectionHandlers) dek(c *fiber.Ctx, orgID uuid.UUID) ([]byte, error) {
	return global.GetOrCreateOrgDEK(c.Context(), h.d.DB, h.d.KEK, orgID)
}

type createConnectionRequest struct {
	Provider string          `json:"provider"`
	Name     string          `json:"name"`
	BaseURL  string          `json:"base_url"`
	Config   json.RawMessage `json:"config"`
	Secret   string          `json:"secret"`
}

func (h connectionHandlers) create(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return fiber.ErrNotFound
	}
	var req createConnectionRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Provider == "" || req.Name == "" || req.BaseURL == "" || req.Secret == "" {
		return fiber.NewError(fiber.StatusBadRequest, "provider, name, base_url, and secret are required")
	}
	if err := netguard.ValidateBaseURL(c.Context(), h.netguardConfig(), req.BaseURL); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "base_url rejected: "+err.Error())
	}

	dek, err := h.dek(c, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	conn, err := repo.CreateConnection(c.Context(), s, dek, repo.NewConnection{
		Provider: req.Provider, Name: req.Name, BaseURL: req.BaseURL, Config: req.Config, Secret: req.Secret,
	})
	if err != nil {
		return writeConnectionErr(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(conn)
}

func (h connectionHandlers) list(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	conns, err := repo.ListConnections(c.Context(), s)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(conns)
}

func (h connectionHandlers) get(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	conn, err := repo.ConnectionByID(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(conn)
}

type updateConnectionMetaRequest struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

func (h connectionHandlers) updateMeta(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	var req updateConnectionMetaRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	conn, err := repo.UpdateConnectionMeta(c.Context(), s, id, req.Name, req.Config)
	if err != nil {
		return writeConnectionErr(c, err)
	}
	return c.JSON(conn)
}

type rotateSecretRequest struct {
	Secret string `json:"secret"`
}

func (h connectionHandlers) rotateSecret(c *fiber.Ctx) error {
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
	var req rotateSecretRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	dek, err := h.dek(c, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	conn, err := repo.UpdateConnectionSecret(c.Context(), s, dek, id, req.Secret)
	if err != nil {
		return writeConnectionErr(c, err)
	}
	return c.JSON(conn)
}

type updateBaseURLRequest struct {
	BaseURL string `json:"base_url"`
	Secret  string `json:"secret"`
}

// updateBaseURL is the *only* way base_url can change (task 2.2) — always
// paired with a fresh secret, and always leaving an audit_events row, so
// hijacking a connection by silently retargeting it is structurally not a
// thing this API can do.
func (h connectionHandlers) updateBaseURL(c *fiber.Ctx) error {
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
	var req updateBaseURLRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if req.BaseURL == "" {
		return fiber.NewError(fiber.StatusBadRequest, "base_url is required")
	}
	if err := netguard.ValidateBaseURL(c.Context(), h.netguardConfig(), req.BaseURL); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "base_url rejected: "+err.Error())
	}

	dek, err := h.dek(c, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	conn, err := repo.UpdateConnectionBaseURLWithSecret(c.Context(), s, dek, id, req.BaseURL, req.Secret)
	if err != nil {
		return writeConnectionErr(c, err)
	}

	var actor *uuid.UUID
	if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
		actor = &p.UserID
	}
	meta, _ := json.Marshal(fiber.Map{"new_base_url": req.BaseURL})
	if err := repo.RecordAuditEvent(c.Context(), s, actor, "connection.base_url_changed", "integration_connection", &id, meta); err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(conn)
}

// test runs task 2.2's Test Connection: decrypts the credential in
// memory, makes one SSRF-guarded live call to the provider, and persists
// only the outcome — never the secret itself, which never leaves this
// function.
func (h connectionHandlers) test(c *fiber.Ctx) error {
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
	conn, err := repo.ConnectionByID(c.Context(), s, id)
	if err != nil {
		return writeRepoErr(c, err)
	}

	dek, err := h.dek(c, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	secret, err := repo.DecryptConnectionSecret(c.Context(), s, dek, id)
	if err != nil {
		return writeRepoErr(c, err)
	}

	client := netguard.NewClient(h.netguardConfig())
	result, testErr := integrations.Test(c.Context(), client, conn.Provider, conn.BaseURL, secret)
	secret = "" // best-effort scrub of the local variable once it's no longer needed

	status, msg := "error", ""
	switch {
	case testErr != nil:
		msg = testErr.Error()
	case result.AuthFailed:
		status, msg = "auth_failed", result.Summary
	case result.OK:
		status, msg = "ok", result.Summary
	default:
		msg = result.Summary
	}
	if err := repo.SetConnectionTestResult(c.Context(), s, id, status, msg); err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(fiber.Map{"status": status, "summary": msg})
}

func writeConnectionErr(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, repo.ErrNameTaken):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "connection name already taken for this provider"})
	case errors.Is(err, repo.ErrSecretRequired):
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "a new secret must be provided"})
	default:
		return writeRepoErr(c, err)
	}
}

func registerConnectionRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	h := connectionHandlers{d: d}
	app.Post("/connections", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.create)
	app.Get("/connections", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.read"), h.list)
	app.Get("/connections/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.read"), h.get)
	app.Patch("/connections/:id", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.updateMeta)
	app.Post("/connections/:id/secret", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.rotateSecret)
	app.Post("/connections/:id/base-url", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.updateBaseURL)
	app.Post("/connections/:id/test", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.test)
}
