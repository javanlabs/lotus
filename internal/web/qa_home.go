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

type qaHomeData struct {
	Rows  []repo.QAProjectSummary
	Cases int
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
		data := qaHomeData{Rows: rows}
		for _, r := range rows {
			data.Cases += r.Cases
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_home.html", httpx.ViewData{
			Title: "QA Tools", Org: org, User: user, Nav: buildNav("qa_tools", len(projects)), Data: data,
		})
	}
}

func registerQARoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/qa", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("qa.read"), qaHome(d))
	app.Get("/projects/:id/qa", qaRoute(requireSession, loadPrincipal, "qa.read", qaProjectPage(d))...)
	app.Get("/projects/:id/qa/cases", qaRoute(requireSession, loadPrincipal, "qa.read", qaCasesPage(d))...)
}
