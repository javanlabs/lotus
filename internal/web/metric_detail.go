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
func relativeTimeStr(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
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

// basePath is this binding's own detail URL — every sub-view (the kind
// pages, /runs) builds its links off this instead of re-formatting IDs.
func (m metricChrome) basePath() string {
	return "/p/" + m.Project.ID.String() + "/c/" + m.Component.ID.String() + "/m/" + m.Binding.ID.String()
}

// resolveMetricChrome loads and cross-validates the three path segments.
// Each lookup is independently RLS-scoped (repo.*ByID), so a binding_id
// from another organization already 404s there; the extra
// parent-matches-child checks below catch a same-org URL whose segments
// were swapped or don't actually nest (e.g. a copy-pasted component id
// under the wrong project) — a structural integrity check, not a second
// security boundary.
func resolveMetricChrome(c *fiber.Ctx, s tenant.Scope) (*metricChrome, error) {
	projectID, err := uuid.Parse(c.Params("project_id"))
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	componentID, err := uuid.Parse(c.Params("component_id"))
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	bindingID, err := uuid.Parse(c.Params("binding_id"))
	if err != nil {
		return nil, fiber.ErrNotFound
	}

	project, err := repo.ProjectByID(c.Context(), s, projectID)
	if err != nil {
		return nil, err
	}
	component, err := repo.ComponentByID(c.Context(), s, componentID)
	if err != nil {
		return nil, err
	}
	if component.ProjectID != project.ID {
		return nil, repo.ErrNotFound
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

// metricDetailPage is GET /p/:project_id/c/:component_id/m/:binding_id —
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
		nav := buildNav("projects", len(projects))
		nav.Trail = componentTrail(&chrome.Project, &chrome.Component,
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
	case "partial", "skipped":
		return "warn"
	case "failed":
		return "fail"
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

		nav := buildNav("projects", len(projects))
		nav.Trail = componentTrail(&chrome.Project, &chrome.Component,
			httpx.NavCrumb{Label: chrome.Binding.Kind, Href: chrome.basePath()},
			httpx.NavCrumb{Label: "Run history"})
		c.Type("html")
		return d.Renderer.Render(c, "pages/metric_runs.html", httpx.ViewData{
			Title: chrome.Component.Name + " run history", Org: org, User: user, Nav: nav,
			Data: struct {
				Chrome    metricChrome
				KindLabel string
				Runs      []runRow
			}{Chrome: *chrome, KindLabel: kindLabels[chrome.Binding.Kind], Runs: buildRunRows(runs)},
		})
	}
}

func registerMetricDetailRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	const base = "/p/:project_id/c/:component_id/m/:binding_id"
	app.Get(base, tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), metricDetailPage(d))
	app.Get(base+"/runs", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), runHistoryPage(d))
	registerTrivyNoteRoutes(app, base, requireSession, loadPrincipal)
	registerMetricChartRoutes(app, base, d, requireSession, loadPrincipal)
}
