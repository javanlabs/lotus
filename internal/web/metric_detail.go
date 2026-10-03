// Package web's metric_detail* files are task 4.4: the four
// integration-specific detail pages, ported from the legacy Lotus
// (/Users/uyab/Code/lotus/web/internal/handlers/{gitlab,sonar,trivy,sentry}.go
// and their templates) and re-keyed from a service/image name (a string)
// to a binding_id (a UUID) — the old /trivy/:image shape was a literal
// cross-tenant IDOR, since an image name is not unique across
// organizations. Every lookup here goes through tenant.Scope, so a
// binding belonging to another org resolves to 404, the same way every
// other resource in this app already does.
package web

import (
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// relativeTimeStr renders a coarse "how long ago", English translation
// of the legacy Lotus's own relativeTime (handlers/sonar.go) — same
// thresholds, since they were a fine choice, just not the Indonesian
// wording this app's UI otherwise doesn't use.
func relativeTimeStr(t time.Time) string { return relativeTimeStrAt(t, time.Now()) }

// relativeTimeStrAt is relativeTimeStr measured from now instead of the
// wall clock.
func relativeTimeStrAt(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// metricChrome is what every one of the four detail pages (and the run
// history page) needs regardless of kind: the resolved, ownership-
// verified project/component/binding/connection chain.
type metricChrome struct {
	Project    repo.Project
	Component  repo.Component
	Binding    repo.Binding
	Connection repo.Connection
}

// BasePath is this binding's own detail URL — every sub-view (the kind
// pages, /runs) and template builds its links off this instead of
// re-formatting slugs and IDs.
func (m metricChrome) BasePath() string {
	return metricURL(&m.Project, &m.Component, m.Binding.ID)
}

// ProjectURL and ComponentURL are the metric header's breadcrumb links.
func (m metricChrome) ProjectURL() string { return projectURL(&m.Project) }

func (m metricChrome) ComponentURL() string { return componentURL(&m.Project, &m.Component) }

// legacyMetricBase is the detail pages' old address, keyed by three ids;
// metricBase replaced it.
const (
	metricBase       = "/projects/:id/components/:cid/metrics/:binding_id"
	legacyMetricBase = "/p/:project_id/c/:component_id/m/:binding_id"
)

// resolveMetricChrome loads and cross-validates the three path segments,
// from metricBase (project and component by slug or id) or
// legacyMetricBase (by id). Each lookup is independently RLS-scoped, so a
// binding_id from another organization already 404s there; the extra
// parent-matches-child checks catch a same-org URL whose segments were
// swapped or don't actually nest (e.g. a copy-pasted component under the
// wrong project) — a structural integrity check, not a second security
// boundary. Anything not found is repo.ErrNotFound, so callers answer it
// with writeRepoErr like any other repo error.
func resolveMetricChrome(c *fiber.Ctx, s tenant.Scope) (*metricChrome, error) {
	pparam, cparam := projectParam, componentParam
	if c.Params("project_id") != "" {
		pparam, cparam = "project_id", legacyComponentParam
	}
	bindingID, err := uuid.Parse(c.Params("binding_id"))
	if err != nil {
		return nil, repo.ErrNotFound
	}

	project, err := lookupProject(c, c.Params(pparam))
	if err != nil {
		return nil, repoNotFound(err)
	}
	component, err := lookupComponent(c, project, c.Params(cparam))
	if err != nil {
		return nil, repoNotFound(err)
	}
	binding, err := repo.BindingByID(c.Context(), s, bindingID)
	if err != nil {
		return nil, err
	}
	if binding.ComponentID != component.ID {
		return nil, repo.ErrNotFound
	}
	connection, err := repo.ConnectionByID(c.Context(), s, binding.ConnectionID)
	if err != nil {
		return nil, err
	}

	return &metricChrome{Project: *project, Component: *component, Binding: *binding, Connection: *connection}, nil
}

// repoNotFound turns the resolvers' fiber 404 back into repo.ErrNotFound.
func repoNotFound(err error) error {
	if errors.Is(err, fiber.ErrNotFound) {
		return repo.ErrNotFound
	}
	return err
}

// metricDetailPage is GET metricBase —
// dispatches to the one of four kind-specific renderers below by the
// binding's own kind. There's no fifth branch that renders "unknown kind"
// generically: every kind this app supports has a Descriptor (task 3.1),
// so an unrecognized kind here would mean a data-integrity bug, not a
// user-reachable state.
func metricDetailPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
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
		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		nav := buildNav("projects", len(projects))
		nav.Trail = componentTrail(folders, &chrome.Project, &chrome.Component,
			httpx.NavCrumb{Label: chrome.Binding.Kind})

		switch chrome.Binding.Kind {
		case "gitlab":
			return renderGitlabDetail(c, d, s, *chrome, org, user, nav)
		case "sonarqube":
			return renderSonarDetail(c, d, s, *chrome, org, user, nav)
		case "trivy":
			return renderTrivyDetail(c, d, s, *chrome, org, user, nav, "")
		case "sentry":
			return renderSentryDetail(c, d, s, *chrome, org, user, nav)
		default:
			return fiber.NewError(fiber.StatusInternalServerError, "unknown binding kind: "+chrome.Binding.Kind)
		}
	}
}

// runRow is one sync_runs entry, flattened for the template — every
// nullable column pre-resolved to a plain string/bool, since
// html/template prints a non-nil pointer as its address, not its
// pointee (the same rule internal/web/component_page.go's bindingRow
// doc comment calls out).
type runRow struct {
	Status      string
	Tone        string // pass|warn|fail|none, reusing the badge package
	Trigger     string
	StartedAt   string
	Duration    string
	ErrorClass  string
	Error       string
	HasWarnings bool
	Warnings    string
}

func runToneFor(status string) string {
	switch status {
	case "ok":
		return "pass"
	case "partial", "failed":
		return "fail"
	case "skipped":
		// Nothing ran, so there is nothing to pass or fail.
		return "none"
	default:
		return "none"
	}
}

func buildRunRows(runs []global.SyncRun) []runRow {
	rows := make([]runRow, 0, len(runs))
	for _, r := range runs {
		row := runRow{Status: r.Status, Tone: runToneFor(r.Status), Trigger: r.Trigger, StartedAt: "–", Duration: "–"}
		if r.StartedAt != nil {
			row.StartedAt = r.StartedAt.Local().Format("2006-01-02 15:04")
			if r.FinishedAt != nil {
				row.Duration = fmtDuration(int(r.FinishedAt.Sub(*r.StartedAt).Seconds()))
			}
		}
		if r.ErrorClass != nil {
			row.ErrorClass = *r.ErrorClass
		}
		if r.Error != nil {
			row.Error = *r.Error
		}
		if len(r.WarningsJSON) > 0 && string(r.WarningsJSON) != "null" {
			row.HasWarnings = true
			row.Warnings = string(r.WarningsJSON)
		}
		rows = append(rows, row)
	}
	return rows
}

// runHistoryPage is GET .../runs — task 4.4's own acceptance criterion:
// status, duration, error class, and any warnings, per attempt.
func runHistoryPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
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

		runs, err := global.ListRunsForBinding(c.Context(), d.DB, s.OrgID(), chrome.Binding.ID, 50)
		if err != nil {
			return writeRepoErr(c, err)
		}

		folders, err := repo.ListFolders(c.Context(), s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		nav := buildNav("projects", len(projects))
		nav.Trail = componentTrail(folders, &chrome.Project, &chrome.Component,
			httpx.NavCrumb{Label: chrome.Binding.Kind, Href: chrome.BasePath()},
			httpx.NavCrumb{Label: "Run history"})
		c.Type("html")
		return renderPage(c, d, "pages/metric_runs.html", httpx.ViewData{
			Title: chrome.Component.Name + " run history", Org: org, User: user, Nav: nav,
			Data: struct {
				Chrome    metricChrome
				KindLabel string
				Runs      []runRow
			}{Chrome: *chrome, KindLabel: kindLabels[chrome.Binding.Kind], Runs: buildRunRows(runs)},
		})
	}
}

// legacyMetricRedirect answers a GET of legacyMetricBase+suffix with 301 to
// the same page under metricBase, keeping the query.
func legacyMetricRedirect(suffix string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		return redirectPermanent(c, chrome.BasePath()+suffix)
	}
}

func registerMetricDetailRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get(metricBase, tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), metricDetailPage(d))
	app.Get(metricBase+"/runs", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), runHistoryPage(d))
	registerTrivyNoteRoutes(app, metricBase, requireSession, loadPrincipal)
	registerMetricChartRoutes(app, metricBase, d, requireSession, loadPrincipal)

	// The old addresses: a GET moves to the slug URL; a Trivy note posted
	// from a page loaded before the move is still taken.
	for _, suffix := range []string{"", "/runs", "/charts"} {
		app.Get(legacyMetricBase+suffix, tenant.RequireTenant(), requireSession, loadPrincipal,
			authz.Require("dashboard.read"), legacyMetricRedirect(suffix))
	}
	registerTrivyNoteRoutes(app, legacyMetricBase, requireSession, loadPrincipal)
}
