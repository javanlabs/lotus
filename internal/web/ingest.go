package web

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// registerIngestRoutes wires task 3.4's push direction: a customer's own
// CI POSTs a Trivy report straight to Lotus, authenticated by a service
// token (task 1.8), rather than Lotus ever reaching into their pipeline.
// Parsed and persisted synchronously — unlike the pull direction (task
// 3.2's queue), a push is inherently a one-shot, out-of-band event with
// no schedule to enqueue against.
func registerIngestRoutes(app *fiber.App, d Deps, authDeps authn.Deps) {
	app.Post("/ingest/trivy/:binding_id", tenant.RequireTenant(),
		authn.RequireTokenOrSession(authDeps), authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB),
		authz.Require("metrics.ingest"),
		ingestTrivy(d))
}

func ingestTrivy(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		bindingID, err := uuid.Parse(c.Params("binding_id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		binding, err := repo.BindingByID(c.Context(), s, bindingID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if binding.Kind != string(collector.KindTrivy) {
			return fiber.ErrNotFound
		}

		// A service token pinned to a specific component (task 1.8) must
		// only ever push into that component — presenting it against any
		// other binding is a 404, not a 403, the same posture every other
		// cross-scope object check in this codebase takes (see
		// authn.RequireTokenOrSession's own doc comment).
		principal, _ := authz.CurrentPrincipal(c)
		if principal.PinnedComponentID != uuid.Nil && principal.PinnedComponentID != binding.ComponentID {
			return fiber.ErrNotFound
		}

		ingestor, ok := collector.GetIngestor(collector.KindTrivy)
		if !ok {
			return fiber.NewError(fiber.StatusInternalServerError, "trivy ingestor not registered")
		}

		settings, err := global.OrgSettings(c.Context(), d.DB, s.OrgID())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "internal error")
		}

		result, err := ingestor.Parse(c.Context(), c.Body(), collector.IngestMeta{
			OrgID: s.OrgID(), ComponentID: binding.ComponentID, BindingID: binding.ID, OrgSettings: settings,
		})
		if err != nil {
			return writeIngestErr(err)
		}

		persist, ok := collector.GetPersister(collector.KindTrivy)
		if !ok {
			return fiber.NewError(fiber.StatusInternalServerError, "trivy persister not registered")
		}
		if err := persist(c.Context(), s, binding.ComponentID, binding.ID, result); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "internal error")
		}

		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true})
	}
}

func writeIngestErr(err error) error {
	switch collector.ClassOf(err) {
	case collector.ErrClassConfig:
		return fiber.NewError(fiber.StatusBadRequest, unwrapMsg(err))
	case collector.ErrClassAuth:
		return fiber.NewError(fiber.StatusUnauthorized, unwrapMsg(err))
	case collector.ErrClassNotFound:
		return fiber.ErrNotFound
	case collector.ErrClassRateLimited:
		return fiber.NewError(fiber.StatusTooManyRequests, unwrapMsg(err))
	case collector.ErrClassTimeout:
		return fiber.NewError(fiber.StatusGatewayTimeout, unwrapMsg(err))
	case collector.ErrClassUpstream:
		return fiber.NewError(fiber.StatusBadGateway, unwrapMsg(err))
	default:
		return fiber.NewError(fiber.StatusInternalServerError, "internal error")
	}
}

func unwrapMsg(err error) string {
	if ce, ok := errors.AsType[*collector.CollectError](err); ok {
		return ce.Error()
	}
	return err.Error()
}
