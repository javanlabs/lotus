package web

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
)

// discoveryHandlers, like connectionHandlers and bindingHandlers, closes
// over Deps for the org DEK / SSRF-guarded client Discover needs.
type discoveryHandlers struct {
	d Deps
}

// run decrypts the connection's credential, calls Discover, and upserts
// the results — the shared body behind both "run discovery now" (POST
// .../discover) and, later, a scheduled trigger (Fase 3; there is no
// sync_runs table yet for this phase to record a run against — see the
// TENSION task logged alongside this file). Returns the discovered count
// and, for SonarQube's no-admin-scope case, a degraded flag rather than an
// error.
func (h discoveryHandlers) run(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return fiber.ErrNotFound
	}
	connID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	conn, err := repo.ConnectionByID(c.Context(), s, connID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	bindingKind, err := integrations.BindingKindForProvider(conn.Provider)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	dek, err := global.GetOrCreateOrgDEK(c.Context(), h.d.DB, h.d.KEK, org.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	secret, err := repo.DecryptConnectionSecret(c.Context(), s, dek, connID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	client := netguard.NewClient(netguard.Config{AllowHTTP: h.d.Cfg.Dev, Resolver: h.d.NetguardResolver, IsBlocked: h.d.NetguardIsBlocked})
	items, discErr := integrations.Discover(c.Context(), client, conn.Provider, conn.BaseURL, secret, conn.Config)
	secret = "" //nolint:ineffassign // best-effort scrub once no longer needed

	if errors.Is(discErr, integrations.DiscoverDegradedNoAdminScope) {
		return c.JSON(fiber.Map{"degraded": true, "reason": "credential lacks admin scope; use free-text external_key + Test Binding instead", "count": 0})
	}
	if discErr != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": discErr.Error()})
	}

	n, err := repo.UpsertDiscoveredResources(c.Context(), s, connID, bindingKind, items)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(fiber.Map{"degraded": false, "count": n})
}

func (h discoveryHandlers) list(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	connID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	filter := repo.DiscoveredResourceFilter{
		ConnectionID:  &connID,
		UnclaimedOnly: c.Query("unclaimed") == "true",
		Search:        c.Query("q"),
	}
	resources, err := repo.ListDiscoveredResources(c.Context(), s, filter)
	if err != nil {
		return writeRepoErr(c, err)
	}
	return c.JSON(resources)
}

func (h discoveryHandlers) setIgnored(ignored bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("resource_id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.SetDiscoveredResourceIgnored(c.Context(), s, id, ignored); err != nil {
			return writeRepoErr(c, err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	}
}

type claimTargetRequest struct {
	DiscoveredResourceID uuid.UUID       `json:"discovered_resource_id"`
	ExistingProjectID    *uuid.UUID      `json:"existing_project_id"`
	NewProjectSlug       string          `json:"new_project_slug"`
	NewProjectName       string          `json:"new_project_name"`
	ExistingComponentID  *uuid.UUID      `json:"existing_component_id"`
	NewComponentSlug     string          `json:"new_component_slug"`
	NewComponentName     string          `json:"new_component_name"`
	NewComponentKind     string          `json:"new_component_kind"`
	BindingConfig        json.RawMessage `json:"binding_config"`
}

type claimRequest struct {
	Claims []claimTargetRequest `json:"claims"`
}

// claim is task 2.4's "nyentang N resource bikin Project + Component +
// Binding sekaligus dalam satu aksi".
func (h discoveryHandlers) claim(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	connID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	conn, err := repo.ConnectionByID(c.Context(), s, connID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	bindingKind, err := integrations.BindingKindForProvider(conn.Provider)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	var req claimRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body")
	}
	if len(req.Claims) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "claims must not be empty")
	}

	targets := make([]repo.ClaimTarget, 0, len(req.Claims))
	for i, rc := range req.Claims {
		if rc.DiscoveredResourceID == uuid.Nil {
			return fiber.NewError(fiber.StatusBadRequest, "claims["+strconv.Itoa(i)+"].discovered_resource_id is required")
		}
		if rc.ExistingProjectID == nil && rc.NewProjectSlug == "" {
			return fiber.NewError(fiber.StatusBadRequest, "claims["+strconv.Itoa(i)+"] must set existing_project_id or new_project_slug")
		}
		if rc.ExistingComponentID == nil && (rc.NewComponentSlug == "" || rc.NewComponentKind == "") {
			return fiber.NewError(fiber.StatusBadRequest, "claims["+strconv.Itoa(i)+"] must set existing_component_id or new_component_slug+new_component_kind")
		}
		targets = append(targets, repo.ClaimTarget{
			DiscoveredResourceID: rc.DiscoveredResourceID,
			ExistingProjectID:    rc.ExistingProjectID,
			NewProjectSlug:       rc.NewProjectSlug,
			NewProjectName:       rc.NewProjectName,
			ExistingComponentID:  rc.ExistingComponentID,
			NewComponentSlug:     rc.NewComponentSlug,
			NewComponentName:     rc.NewComponentName,
			NewComponentKind:     rc.NewComponentKind,
			BindingConfig:        rc.BindingConfig,
		})
	}

	results, err := repo.ClaimDiscoveredResources(c.Context(), s, connID, bindingKind, targets)
	if err != nil {
		return writeBindingErr(c, err)
	}
	// Same "data within seconds" behavior as a single binding creation
	// (task 3.2) — every binding this bulk claim just created gets its
	// own immediate run.
	for _, r := range results {
		_ = scheduler.EnqueueImmediate(c.Context(), h.d.DB, s.OrgID(), connID, r.BindingID, bindingKind)
	}
	return c.Status(fiber.StatusCreated).JSON(results)
}

func registerDiscoveryRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	h := discoveryHandlers{d: d}
	app.Post("/connections/:id/discover", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), h.run)
	app.Get("/connections/:id/discovered", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), h.list)
	app.Post("/discovered/:resource_id/ignore", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.setIgnored(true))
	app.Post("/discovered/:resource_id/unignore", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.setIgnored(false))
	app.Post("/connections/:id/discovered/claim", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.claim)
}
