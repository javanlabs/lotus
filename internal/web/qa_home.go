package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaProjectScope is the project list a principal's QA views are limited to:
// nil (every project) for members and above, the listed projects for a
// guest — never nil for a guest, so a guest on no projects sees nothing.
func qaProjectScope(c *fiber.Ctx, s tenant.Scope) ([]uuid.UUID, error) {
	p := mustPrincipal(c)
	if !authz.IsProjectScoped(p) {
		return nil, nil
	}
	return repo.ListProjectIDsForMember(c.Context(), s, p.UserID)
}

// qaHomeRow is one project's line on /qa, with the link to its QA area.
type qaHomeRow struct {
	repo.QAProjectSummary
	Href string
}

type qaHomeData struct {
	Rows        []qaHomeRow
	Cases       int
	CanWorkload bool // qa.workload.read: the header links to /qa/workload
}

// qaHome is GET /qa: every project the principal can reach, with its QA
// counts. The sidebar's QA Tools entry opens it.
func qaHome(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		ids, err := qaProjectScope(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		rows, err := repo.ListQAProjectSummaries(c.Context(), s, ids)
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
		data := qaHomeData{Rows: make([]qaHomeRow, 0, len(rows)), CanWorkload: mustPrincipal(c).Can("qa.workload.read")}
		for _, r := range rows {
			data.Rows = append(data.Rows, qaHomeRow{QAProjectSummary: r, Href: qaBaseFor(r.ProjectID, r.ProjectSlug)})
			data.Cases += r.Cases
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_home.html", httpx.ViewData{
			Title: "QA Tools", Org: org, User: user, Nav: buildNav("qa_tools", len(projects)), Data: data,
		})
	}
}

func registerQARoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	registerProjectGuestRoutes(app, d, requireSession, loadPrincipal)
	registerQAShareRoutes(app, d, requireSession, loadPrincipal)
	app.Get("/qa", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("qa.read"), qaHome(d))
	app.Get("/qa/my", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("qa.read"), qaMyPage(d))
	app.Get("/qa/workload", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("qa.workload.read"), qaWorkloadPage(d))
	app.Get("/projects/:id/qa", qaRoute(requireSession, loadPrincipal, "qa.read", qaProjectPage(d))...)
	app.Get("/projects/:id/qa/cases", qaRoute(requireSession, loadPrincipal, "qa.read", qaCasesPage(d))...)
	app.Post("/projects/:id/qa/cases/move", qaRoute(requireSession, loadPrincipal, "qa.case.write", moveQACasesUI(d))...)
	app.Get("/projects/:id/qa/cases/import", qaRoute(requireSession, loadPrincipal, "qa.case.write", qaImportPage(d))...)
	app.Post("/projects/:id/qa/cases/import", qaRoute(requireSession, loadPrincipal, "qa.case.write", previewQAImportUI(d))...)
	app.Post("/projects/:id/qa/cases/import/confirm", qaRoute(requireSession, loadPrincipal, "qa.case.write", confirmQAImportUI(d))...)
	app.Get("/projects/:id/qa/cases/export", qaRoute(requireSession, loadPrincipal, "qa.read", exportQACasesUI(d))...)
	app.Get("/projects/:id/qa/cases/new", qaRoute(requireSession, loadPrincipal, "qa.case.write", newQACasePage(d))...)
	app.Post("/projects/:id/qa/cases/new", qaRoute(requireSession, loadPrincipal, "qa.case.write", createQACaseUI(d))...)
	app.Get("/projects/:id/qa/cases/:key", qaRoute(requireSession, loadPrincipal, "qa.read", qaCasePage(d))...)
	app.Post("/projects/:id/qa/cases/:key", qaRoute(requireSession, loadPrincipal, "qa.case.write", updateQACaseUI(d))...)
	app.Post("/projects/:id/qa/cases/:key/archive", qaRoute(requireSession, loadPrincipal, "qa.case.write", setQACaseArchivedUI(true))...)
	app.Post("/projects/:id/qa/cases/:key/restore", qaRoute(requireSession, loadPrincipal, "qa.case.write", setQACaseArchivedUI(false))...)
	app.Get("/projects/:id/qa/scenarios", qaRoute(requireSession, loadPrincipal, "qa.read", qaScenariosPage(d))...)
	app.Post("/projects/:id/qa/scenarios", qaRoute(requireSession, loadPrincipal, "qa.case.write", createQAScenarioUI(d))...)
	app.Get("/projects/:id/qa/scenarios/:sid", qaRoute(requireSession, loadPrincipal, "qa.read", qaScenarioPage(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid", qaRoute(requireSession, loadPrincipal, "qa.case.write", updateQAScenarioUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/archive", qaRoute(requireSession, loadPrincipal, "qa.case.write", archiveQAScenarioUI())...)
	app.Get("/projects/:id/qa/scenarios/:sid/picker", qaRoute(requireSession, loadPrincipal, "qa.case.write", qaScenarioPickerUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/groups", qaRoute(requireSession, loadPrincipal, "qa.case.write", addQAScenarioGroupUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/groups/:gid/rename", qaRoute(requireSession, loadPrincipal, "qa.case.write", renameQAScenarioGroupUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/groups/:gid/move", qaRoute(requireSession, loadPrincipal, "qa.case.write", moveQAScenarioGroupUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/cases", qaRoute(requireSession, loadPrincipal, "qa.case.write", addQAScenarioCasesUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/cases/remove", qaRoute(requireSession, loadPrincipal, "qa.case.write", removeQAScenarioCasesUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/items/group", qaRoute(requireSession, loadPrincipal, "qa.case.write", regroupQAScenarioItemsUI(d))...)
	app.Post("/projects/:id/qa/scenarios/:sid/items/:iid/move", qaRoute(requireSession, loadPrincipal, "qa.case.write", moveQAScenarioItemUI(d))...)
	app.Get("/projects/:id/qa/plans", qaRoute(requireSession, loadPrincipal, "qa.read", qaPlansPage(d))...)
	app.Post("/projects/:id/qa/plans", qaRoute(requireSession, loadPrincipal, "qa.plan.write", createQAPlanUI(d))...)
	app.Get("/projects/:id/qa/plans/:pid", qaRoute(requireSession, loadPrincipal, "qa.read", qaPlanPage(d))...)
	app.Post("/projects/:id/qa/plans/:pid", qaRoute(requireSession, loadPrincipal, "qa.plan.write", updateQAPlanUI(d))...)
	app.Post("/projects/:id/qa/plans/:pid/duplicate", qaRoute(requireSession, loadPrincipal, "qa.plan.write", duplicateQAPlanUI(d))...)
	app.Post("/projects/:id/qa/plans/:pid/signoffs", qaRoute(requireSession, loadPrincipal, "qa.signoff", recordQASignoffUI(d))...)
	app.Post("/projects/:id/qa/plans/:pid/close", qaRoute(requireSession, loadPrincipal, "qa.plan.write", closeQAPlanUI())...)
	app.Get("/projects/:id/qa/plans/:pid/runs/new", qaRoute(requireSession, loadPrincipal, "qa.plan.write", newQARunPage(d))...)
	app.Post("/projects/:id/qa/plans/:pid/runs", qaRoute(requireSession, loadPrincipal, "qa.plan.write", createQARunUI(d))...)
	app.Post("/projects/:id/qa/plans/:pid/runs/preview", qaRoute(requireSession, loadPrincipal, "qa.plan.write", previewQARunUI(d))...)
	app.Get("/projects/:id/qa/runs/:rid", qaRoute(requireSession, loadPrincipal, "qa.read", qaRunPage(d))...)
	app.Post("/projects/:id/qa/runs/:rid", qaRoute(requireSession, loadPrincipal, "qa.plan.write", updateQARunUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/delete", qaRoute(requireSession, loadPrincipal, "qa.plan.write", deleteQARunUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/items/remove", qaRoute(requireSession, loadPrincipal, "qa.plan.write", removeQARunItemsUI(d))...)
	app.Get("/projects/:id/qa/runs/:rid/items/:iid", qaRoute(requireSession, loadPrincipal, "qa.read", qaItemPanelUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/items/:iid/result", qaRoute(requireSession, loadPrincipal, "qa.execute", recordQAResultUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/assign", qaRoute(requireSession, loadPrincipal, "qa.plan.write", assignQAItemsUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/retest", qaRoute(requireSession, loadPrincipal, "qa.plan.write", retestQARunUI(d))...)
	app.Post("/projects/:id/qa/runs/:rid/close", qaRoute(requireSession, loadPrincipal, "qa.plan.write", closeQARunUI())...)
	app.Post("/projects/:id/qa/runs/:rid/scenario", qaRoute(requireSession, loadPrincipal, "qa.case.write", saveQARunAsScenarioUI(d))...)
	app.Get("/projects/:id/qa/files/:aid", qaRoute(requireSession, loadPrincipal, "qa.read", qaFileUI(d))...)
	app.Get("/projects/:id/qa/issues", qaRoute(requireSession, loadPrincipal, "qa.read", qaIssuesPage(d))...)
	app.Get("/projects/:id/qa/issues/new", qaRoute(requireSession, loadPrincipal, "qa.issue.report", newQAIssuePage(d))...)
	app.Post("/projects/:id/qa/issues/new", qaRoute(requireSession, loadPrincipal, "qa.issue.report", createQAIssueUI(d))...)
	app.Get("/projects/:id/qa/issues/:key", qaRoute(requireSession, loadPrincipal, "qa.read", qaIssuePage(d))...)
	app.Post("/projects/:id/qa/issues/:key", qaRoute(requireSession, loadPrincipal, "qa.issue.write", updateQAIssueUI(d))...)
	app.Post("/projects/:id/qa/issues/:key/status", qaRoute(requireSession, loadPrincipal, "qa.issue.write", changeQAIssueStatusUI(d))...)
	app.Post("/projects/:id/qa/issues/:key/comments", qaRoute(requireSession, loadPrincipal, "qa.issue.report", commentQAIssueUI(d))...)
	app.Post("/projects/:id/qa/sections", qaRoute(requireSession, loadPrincipal, "qa.case.write", createQASectionUI(d))...)
	app.Post("/projects/:id/qa/sections/:sid/rename", qaRoute(requireSession, loadPrincipal, "qa.case.write", renameQASectionUI(d))...)
	app.Post("/projects/:id/qa/sections/:sid/move", qaRoute(requireSession, loadPrincipal, "qa.case.write", moveQASectionUI(d))...)
	app.Post("/projects/:id/qa/sections/:sid/archive", qaRoute(requireSession, loadPrincipal, "qa.case.write", archiveQASectionUI(d))...)
}
