package web

import (
	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

type qaProjectData struct {
	Project repo.Project
	Base    string
	Summary repo.QAProjectSummary
}

// qaProjectPage is GET /projects/:id/qa: the project's QA counts, and the
// way into its cases and scenarios.
func qaProjectPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		summary, err := repo.QASummaryForProject(c.Context(), s, project.ID)
		if err != nil && err != repo.ErrNotFound { // an archived project has no summary row
			return writeRepoErr(c, err)
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project)
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_project.html", httpx.ViewData{
			Title: project.Name + " · QA", Org: org, User: user, Nav: nav,
			Data: qaProjectData{Project: *project, Base: qaBase(project), Summary: summary},
		})
	}
}
