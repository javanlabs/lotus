package web

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/integrations"
	"github.com/uyab/lotus/internal/netguard"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
)

// bindWizardHandlers, like connectionHandlers, needs Deps for the org DEK
// and SSRF guard a new connection's creation may require.
type bindWizardHandlers struct{ d Deps }

type bindWizardData struct {
	ComponentID   uuid.UUID
	ComponentName string
	Kind          string
	Kinds         []string
	KindLabels    map[string]string

	ExternalKeyLabel string
	ExternalKey      string
	BindingFields    []fieldView

	Connections       []repo.Connection
	ConnectionID      string // "" | "new" | a connection uuid string — preserves the posted choice on re-render
	NewConnectionName string
	ConnectionFields  []fieldView

	Error string
}

func (h bindWizardHandlers) get(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	componentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	comp, err := repo.ComponentByID(c.Context(), s, componentID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	kind := c.Query("kind")
	if !isValidKind(kind) {
		kind = bindableKinds[0]
	}
	data := bindWizardData{
		ComponentID: comp.ID, ComponentName: comp.Name, Kind: kind, Kinds: bindableKinds, KindLabels: kindLabels,
		ConnectionID: "new",
	}
	return h.render(c, data, fiber.StatusOK)
}

func (h bindWizardHandlers) post(c *fiber.Ctx) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return fiber.ErrNotFound
	}
	componentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrNotFound
	}
	comp, err := repo.ComponentByID(c.Context(), s, componentID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	formVal := func(key string) string { return c.FormValue(key) }

	kind := c.FormValue("kind")
	if !isValidKind(kind) {
		return fiber.NewError(fiber.StatusBadRequest, "unknown kind")
	}
	connectionID := c.FormValue("connection_id")
	externalKey := strings.TrimSpace(c.FormValue("external_key"))

	data := bindWizardData{
		ComponentID: comp.ID, ComponentName: comp.Name, Kind: kind, Kinds: bindableKinds, KindLabels: kindLabels,
		ConnectionID: connectionID, ExternalKey: externalKey,
		NewConnectionName: strings.TrimSpace(c.FormValue("new_connection_name")),
	}

	if externalKey == "" {
		data.Error = "the external key is required"
		return h.render(c, data, fiber.StatusUnprocessableEntity)
	}

	var connID uuid.UUID
	if connectionID == "new" {
		if data.NewConnectionName == "" {
			data.Error = "a connection name is required"
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
		baseURL, secret, config := planConnectionFields(kind, formVal)
		if baseURL == "" || secret == "" {
			data.Error = "every connection field is required"
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
		netguardCfg := netguard.Config{AllowHTTP: h.d.Cfg.Dev, Resolver: h.d.NetguardResolver, IsBlocked: h.d.NetguardIsBlocked}
		if err := netguard.ValidateBaseURL(c.Context(), netguardCfg, baseURL); err != nil {
			data.Error = "connection URL rejected: " + err.Error()
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
		configJSON, err := marshalConfig(config)
		if err != nil {
			data.Error = err.Error()
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
		dek, err := global.GetOrCreateOrgDEK(c.Context(), h.d.DB, h.d.KEK, org.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		conn, err := repo.CreateConnection(c.Context(), s, dek, repo.NewConnection{
			Provider: providerForKind(kind), Name: data.NewConnectionName, BaseURL: baseURL, Config: configJSON, Secret: secret,
		})
		if err != nil {
			data.Error = connectionCreateErrorMessage(err)
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
		connID = conn.ID
	} else {
		connID, err = uuid.Parse(connectionID)
		if err != nil {
			data.Error = "choose a connection"
			return h.render(c, data, fiber.StatusUnprocessableEntity)
		}
	}

	descriptor, err := integrations.DescriptorFor(kind)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	bindingConfig, err := buildBindingConfig(descriptor.ConfigFields(), formVal)
	if err != nil {
		data.Error = err.Error()
		return h.render(c, data, fiber.StatusUnprocessableEntity)
	}

	binding, err := repo.CreateBinding(c.Context(), s, repo.NewBinding{
		ComponentID: comp.ID, ConnectionID: connID, Kind: kind, ExternalKey: externalKey, Config: bindingConfig,
	})
	if err != nil {
		data.Error = bindingCreateErrorMessage(err)
		return h.render(c, data, fiber.StatusUnprocessableEntity)
	}
	_ = scheduler.EnqueueImmediate(c.Context(), h.d.DB, s.OrgID(), binding.ConnectionID, binding.ID, binding.Kind)

	return c.Redirect("/components/"+comp.ID.String(), fiber.StatusFound)
}

func marshalConfig(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(m)
}

func connectionCreateErrorMessage(err error) string {
	if errors.Is(err, repo.ErrNameTaken) {
		return "a connection with that name already exists for this provider"
	}
	return err.Error()
}

func bindingCreateErrorMessage(err error) string {
	if errors.Is(err, repo.ErrBindingTargetTaken) {
		return err.Error()
	}
	if errors.Is(err, repo.ErrNotFound) {
		return "connection not found"
	}
	return err.Error()
}

func (h bindWizardHandlers) render(c *fiber.Ctx, data bindWizardData, status int) error {
	s, err := mustScope(c)
	if err != nil {
		return err
	}
	org, user, err := shellChrome(c, h.d.DB)
	if err != nil {
		return err
	}

	provider := providerForKind(data.Kind)
	conns, err := repo.ListConnections(c.Context(), s)
	if err != nil {
		return writeRepoErr(c, err)
	}
	for _, conn := range conns {
		if conn.Provider == provider {
			data.Connections = append(data.Connections, conn)
		}
	}
	if data.ConnectionID == "" {
		if len(data.Connections) > 0 {
			data.ConnectionID = data.Connections[0].ID.String()
		} else {
			data.ConnectionID = "new"
		}
	}

	descriptor, err := integrations.DescriptorFor(data.Kind)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	data.ExternalKeyLabel = descriptor.ExternalKeyLabel()
	data.BindingFields = bindingFieldViews(descriptor.ConfigFields(), nil)
	data.ConnectionFields = connectionFieldViews(connectionFieldsFor(data.Kind), nil)

	projects, err := repo.ListProjects(c.Context(), s, false)
	if err != nil {
		return writeRepoErr(c, err)
	}

	c.Type("html")
	c.Status(status)
	return h.d.Renderer.Render(c, "pages/bind_wizard.html", httpx.ViewData{
		Title: "Bind metric", Org: org, User: user, Nav: buildNav("projects", len(projects)),
		Data: data,
	})
}

func registerBindWizardRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	h := bindWizardHandlers{d: d}
	app.Get("/components/:id/bind", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.get)
	app.Post("/components/:id/bind", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), h.post)
}
