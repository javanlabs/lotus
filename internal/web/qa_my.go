package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaNavCount puts the number of untested items assigned to the viewer on
// the sidebar's QA Tools entry. It runs for every full page (renderPage);
// a failure only leaves the count off.
func qaNavCount(c *fiber.Ctx, nav *httpx.Nav) {
	p, ok := authz.CurrentPrincipal(c)
	if !ok || !p.Can("qa.read") {
		return
	}
	s, ok := tenant.FromContext(c)
	if !ok {
		return
	}
	ids, err := qaProjectScope(c, s)
	if err != nil {
		return
	}
	n, err := repo.CountMyQAItems(c.Context(), s, p.UserID, ids)
	if err != nil || n == 0 {
		return
	}
	for i := range nav.Items {
		if nav.Items[i].Key == "qa_tools" {
			nav.Items[i].Count = strconv.Itoa(n)
		}
	}
}

type qaMyRun struct {
	Name  string
	Href  string
	Items []repo.QAMyItem
}

type qaMyPlan struct {
	Name string
	Href string
	Runs []qaMyRun
}

type qaMyProject struct {
	Name  string
	Href  string
	Plans []qaMyPlan
}

type qaMyData struct {
	Projects    []qaMyProject
	Total       int
	CanWorkload bool // qa.workload.read: the header links to /qa/workload
}

// qaMyPage is GET /qa/my: the untested items assigned to the viewer in open
// runs of open plans, by project, plan and run.
func qaMyPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		ids, err := qaProjectScope(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		items, err := repo.ListMyQAItems(c.Context(), s, mustPrincipal(c).UserID, ids)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data := qaMyData{Total: len(items), CanWorkload: mustPrincipal(c).Can("qa.workload.read")}
		var lastProject, lastPlan, lastRun uuid.UUID
		for _, it := range items {
			base := "/projects/" + it.ProjectID.String() + "/qa"
			if it.ProjectID != lastProject {
				data.Projects = append(data.Projects, qaMyProject{Name: it.ProjectName, Href: base})
				lastProject, lastPlan, lastRun = it.ProjectID, uuid.Nil, uuid.Nil
			}
			pr := &data.Projects[len(data.Projects)-1]
			if it.PlanID != lastPlan {
				pr.Plans = append(pr.Plans, qaMyPlan{Name: it.PlanName, Href: base + "/plans/" + it.PlanID.String()})
				lastPlan, lastRun = it.PlanID, uuid.Nil
			}
			pl := &pr.Plans[len(pr.Plans)-1]
			if it.RunID != lastRun {
				pl.Runs = append(pl.Runs, qaMyRun{Name: it.RunName, Href: base + "/runs/" + it.RunID.String() + "?who=mine"})
				lastRun = it.RunID
			}
			ru := &pl.Runs[len(pl.Runs)-1]
			ru.Items = append(ru.Items, it)
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_my.html", httpx.ViewData{
			Title: "My work", Org: org, User: user, Nav: navWithTrail("qa_tools", len(projects), []httpx.NavCrumb{{Label: "QA Tools", Href: "/qa"}, {Label: "My work"}}), Data: data,
		})
	}
}
