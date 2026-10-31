package web

import (
	"encoding/json"
	"strconv"
	"strings"

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

// connectionRow is a connection flattened for the settings list — task
// 2.2's write-only secret rule means this never carries anything beyond
// last4/fingerprint-derived display fields, the same shape Connection
// itself already enforces at the repo layer; this just adds the derived
// display strings (relative times, tone, affected-binding count) a
// template shouldn't compute itself.
type connectionRow struct {
	ID              string
	Provider        string
	ProviderLabel   string
	Name            string
	BaseURL         string
	StatusLabel     string
	StatusTone      string
	SecretLast4     string
	SecretUpdatedAt string
	LastVerifiedAt  string
	LastError       string
	ActiveBindings  int
}

func providerLabel(provider string) string {
	for kind, label := range connectionLabels {
		if providerForKind(kind) == provider {
			return label
		}
	}
	return provider
}

func statusTone(status string) string {
	switch status {
	case "ok":
		return "pass"
	case "pending":
		return "none"
	default:
		return "fail"
	}
}

func buildConnectionRow(conn repo.Connection, activeBindings int) connectionRow {
	row := connectionRow{
		ID: conn.ID.String(), Provider: conn.Provider, ProviderLabel: providerLabel(conn.Provider),
		Name: conn.Name, BaseURL: conn.BaseURL, StatusLabel: conn.Status, StatusTone: statusTone(conn.Status),
		SecretUpdatedAt: "–", LastVerifiedAt: "–", ActiveBindings: activeBindings,
	}
	if conn.SecretLast4 != nil {
		row.SecretLast4 = "•••• " + *conn.SecretLast4
	}
	if conn.SecretUpdatedAt != nil {
		row.SecretUpdatedAt = relativeTimeStr(*conn.SecretUpdatedAt)
	}
	if conn.LastVerifiedAt != nil {
		row.LastVerifiedAt = relativeTimeStr(*conn.LastVerifiedAt)
	}
	if conn.LastError != nil {
		row.LastError = *conn.LastError
	}
	return row
}

func settingsConnectionsPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
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
		conns, err := repo.ListConnections(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		rows := make([]connectionRow, 0, len(conns))
		for _, conn := range conns {
			n, err := repo.CountActiveBindingsForConnection(c.Context(), s, conn.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			rows = append(rows, buildConnectionRow(conn, n))
		}

		kind := c.Query("kind", bindableKinds[0])

		nav := buildNav("", len(projects))
		nav.Trail = []httpx.NavCrumb{{Label: "Settings"}}
		c.Type("html")
		return renderPage(c, d, "pages/settings_connections.html", httpx.ViewData{
			Title: "Connections", Org: org, User: user, Nav: nav,
			Data: struct {
				SettingsNav []settingsNavItem
				Connections []connectionRow
				Form        connectionFormView
				CanWrite    bool
			}{
				SettingsNav: buildSettingsNav("connections"), Connections: rows,
				Form:     buildConnectionForm(kind, "", nil, ""),
				CanWrite: mustPrincipal(c).Can("connection.secret.write"),
			},
		})
	}
}

func mustPrincipal(c *fiber.Ctx) authz.Principal {
	p, _ := authz.CurrentPrincipal(c)
	return p
}

// createConnectionSettingsUI is the standalone-connection form task 4.5's
// spec calls for — the same kind-tab + descriptor-driven field set the
// bind wizard already uses (planConnectionFields/connectionFieldsFor,
// bindview.go), just not paired with creating a binding in the same
// submit, since a settings-page connection may be created before any
// component is ready to bind it.
// connectionFormView is the New-connection modal's body. Page render and
// htmx fragment render share it, so switching kind and recovering from a
// rejected submit go through exactly one template.
type connectionFormView struct {
	Kinds        []string
	KindLabels   map[string]string
	SelectedKind string
	Fields       []fieldView
	Name         string
	// NamePlaceholder is the example name for the selected kind. The
	// template used to hardcode one, so it kept saying "e.g. gitlab.com"
	// on the SonarQube, Trivy and Sentry tabs.
	NamePlaceholder string
	Error           string
}

func buildConnectionForm(kind, name string, values map[string]string, errMsg string) connectionFormView {
	if !isValidKind(kind) {
		kind = bindableKinds[0]
	}
	return connectionFormView{
		Kinds: bindableKinds, KindLabels: connectionLabels, SelectedKind: kind,
		Fields: connectionFieldViews(connectionFieldsFor(kind), values),
		Name:   name, NamePlaceholder: connectionNamePlaceholder(kind), Error: errMsg,
	}
}

// submittedConnectionValues reads back what the user typed, so a rejected
// submit re-renders with their input rather than an empty form.
func submittedConnectionValues(c *fiber.Ctx, kind string) map[string]string {
	values := map[string]string{}
	for _, f := range connectionFieldsFor(kind) {
		values[f.Key] = c.FormValue(f.Key)
	}
	return values
}

// connectionFormFragment serves GET /settings/connections/form — the kind
// switcher inside the modal, which swaps the body instead of navigating
// so the dialog stays open.
func connectionFormFragment(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Type("html")
		return d.Renderer.RenderNamed(c, "connection_form",
			buildConnectionForm(c.Query("kind"), c.Query("name"), nil, ""))
	}
}

// rejectConnectionForm is the modal's error path. It answers htmx with the
// form re-rendered inside the modal — 200, because htmx does not swap 4xx
// by default and the point of this path is that the dialog stays open with
// the user's input intact. A non-htmx post (no JS) keeps the plain error
// the handler always returned.
func rejectConnectionForm(c *fiber.Ctx, d Deps, kind, name, msg string) error {
	if c.Get("HX-Request") == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, msg)
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "connection_form",
		buildConnectionForm(kind, name, submittedConnectionValues(c, kind), msg))
}

func createConnectionSettingsUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		kind := c.FormValue("kind")
		if !isValidKind(kind) {
			return fiber.NewError(fiber.StatusBadRequest, "unknown kind")
		}
		name := strings.TrimSpace(c.FormValue("name"))
		if name == "" {
			return rejectConnectionForm(c, d, kind, name, "A connection name is required.")
		}
		formVal := func(key string) string { return c.FormValue(key) }
		baseURL, secret, config := planConnectionFields(kind, formVal)
		if baseURL == "" || secret == "" {
			return rejectConnectionForm(c, d, kind, name, "Every connection field is required.")
		}
		netguardCfg := netguard.Config{AllowHTTP: d.Cfg.Dev, Resolver: d.NetguardResolver, IsBlocked: d.NetguardIsBlocked}
		if err := netguard.ValidateBaseURL(c.Context(), netguardCfg, baseURL); err != nil {
			return rejectConnectionForm(c, d, kind, name, "Connection URL rejected · "+err.Error())
		}
		configJSON, err := marshalConfig(config)
		if err != nil {
			return rejectConnectionForm(c, d, kind, name, err.Error())
		}
		dek, err := global.GetOrCreateOrgDEK(c.Context(), d.DB, d.KEK, org.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if _, err := repo.CreateConnection(c.Context(), s, dek, repo.NewConnection{
			Provider: providerForKind(kind), Name: name, BaseURL: baseURL, Config: configJSON, Secret: secret,
		}); err != nil {
			return rejectConnectionForm(c, d, kind, name, connectionCreateErrorMessage(err))
		}
		setFlash(c, "connection.created")
		return redirectAfterMutation(c, "/settings/connections")
	}
}

func rotateConnectionSecretUI(d Deps) fiber.Handler {
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
		conn, err := repo.ConnectionByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		secret := strings.TrimSpace(c.FormValue("secret"))
		if conn.Provider == providerForKind("trivy") {
			// Harbor's one secret is two fields, as in planConnectionFields.
			// A single box here stored a bare password that could never sign in.
			user, pass := strings.TrimSpace(c.FormValue("username")), strings.TrimSpace(c.FormValue("password"))
			secret = ""
			if user != "" && pass != "" {
				secret = user + ":" + pass
			}
		}
		if secret == "" {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "a new credential must be provided")
		}
		dek, err := global.GetOrCreateOrgDEK(c.Context(), d.DB, d.KEK, org.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		baseURL := strings.TrimRight(strings.TrimSpace(c.FormValue("base_url")), "/")
		if baseURL == "" || baseURL == conn.BaseURL {
			if _, err := repo.UpdateConnectionSecret(c.Context(), s, dek, id, secret); err != nil {
				return writeRepoErr(c, err)
			}
			setFlash(c, "connection.rotated")
			return redirectAfterMutation(c, "/settings/connections")
		}
		// A server that moved. The URL only ever changes together with the
		// credential, so a stored one is never sent to a new host.
		netguardCfg := netguard.Config{AllowHTTP: d.Cfg.Dev, Resolver: d.NetguardResolver, IsBlocked: d.NetguardIsBlocked}
		if err := netguard.ValidateBaseURL(c.Context(), netguardCfg, baseURL); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "Connection URL rejected · "+err.Error())
		}
		if _, err := repo.UpdateConnectionBaseURLWithSecret(c.Context(), s, dek, id, baseURL, secret); err != nil {
			return writeRepoErr(c, err)
		}
		var actor *uuid.UUID
		if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
			actor = &p.UserID
		}
		meta, _ := json.Marshal(fiber.Map{"new_base_url": baseURL})
		if err := repo.RecordAuditEvent(c.Context(), s, actor, "connection.base_url_changed", "integration_connection", &id, meta); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "connection.rotated")
		return redirectAfterMutation(c, "/settings/connections")
	}
}

// testConnectionSettingsUI is an htmx endpoint mirroring component_page.go's
// testBindingUI — same underlying call as connectionHandlers.test (task
// 2.2's JSON API), rendered as an HTML fragment for a human instead of a
// script.
func testConnectionSettingsUI(d Deps) fiber.Handler {
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
		conn, err := repo.ConnectionByID(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		dek, err := global.GetOrCreateOrgDEK(c.Context(), d.DB, d.KEK, org.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		secret, err := repo.DecryptConnectionSecret(c.Context(), s, dek, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		client := netguard.NewClient(netguard.Config{AllowHTTP: d.Cfg.Dev, Resolver: d.NetguardResolver, IsBlocked: d.NetguardIsBlocked})
		result, testErr := integrations.Test(c.Context(), client, conn.Provider, conn.BaseURL, secret)
		secret = "" //nolint:ineffassign // best-effort scrub once no longer needed

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
		c.Type("html")
		return d.Renderer.RenderPartial(c, "partials/binding_test_result.html", struct {
			OK       bool
			AuthFail bool
			Summary  string
			ReqErr   string
		}{OK: status == "ok", AuthFail: status == "auth_failed", Summary: msg, ReqErr: errString(testErr)})
	}
}

// deleteConnectionUI carries out task 4.5's delete flow. The
// how-many-bindings-will-die count is computed and shown client-side
// (settings_connections.html embeds each row's ActiveBindings into its
// own confirm() prompt, the same pattern component_page.go's binding
// delete already uses) — this handler is the actual delete, trusting the
// browser-side confirm the same way every other destructive action in
// this app already does.
func deleteConnectionUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		bindingsDeleted, err := repo.DeleteConnection(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		var actor *uuid.UUID
		if p, ok := authz.CurrentPrincipal(c); ok && p.UserID != uuid.Nil {
			actor = &p.UserID
		}
		meta, _ := json.Marshal(fiber.Map{"bindings_deleted": bindingsDeleted})
		if err := repo.RecordAuditEvent(c.Context(), s, actor, "connection.deleted", "integration_connection", &id, meta); err != nil {
			return writeRepoErr(c, err)
		}
		if bindingsDeleted > 0 {
			setFlash(c, "connection.unbound", strconv.Itoa(bindingsDeleted))
		} else {
			setFlash(c, "connection.deleted")
		}
		return c.Redirect("/settings/connections", fiber.StatusFound)
	}
}

func registerConnectionSettingsRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/settings/connections", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.read"), settingsConnectionsPage(d))
	app.Get("/settings/connections/form", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), connectionFormFragment(d))
	app.Post("/settings/connections", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), createConnectionSettingsUI(d))
	app.Post("/settings/connections/:id/rotate", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), rotateConnectionSecretUI(d))
	app.Post("/settings/connections/:id/test-ui", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), testConnectionSettingsUI(d))
	app.Post("/settings/connections/:id/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("connection.secret.write"), deleteConnectionUI())
}
