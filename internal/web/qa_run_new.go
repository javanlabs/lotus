package web

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// qaPerson is someone who can be given run items: a member with
// qa.execute (owner, admin, member) or a guest listed on the project.
// Viewers cannot execute and are never offered.
type qaPerson struct {
	ID    uuid.UUID
	Label string
	Guest bool
}

func qaPeople(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project) ([]qaPerson, error) {
	members, err := global.ListMembersForOrg(c.Context(), d.DB, s.OrgID())
	if err != nil {
		return nil, err
	}
	guests, err := repo.ListProjectMemberUserIDs(c.Context(), s, project.ID)
	if err != nil {
		return nil, err
	}
	listed := map[uuid.UUID]bool{}
	for _, id := range guests {
		listed[id] = true
	}
	var out []qaPerson
	for _, m := range members {
		label := m.Name
		if strings.TrimSpace(label) == "" {
			label = m.Email
		}
		switch {
		case m.RoleKey == "guest" && listed[m.UserID]:
			out = append(out, qaPerson{ID: m.UserID, Label: label, Guest: true})
		case m.RoleKey == "owner" || m.RoleKey == "admin" || m.RoleKey == "member":
			out = append(out, qaPerson{ID: m.UserID, Label: label})
		}
	}
	return out, nil
}

type qaRunNewData struct {
	Project      repo.Project
	Base         string
	Plan         repo.QAPlan
	Name         string
	Notes        string
	Scenarios    []qaPlanScenarioOption
	People       []qaPlanPersonOption
	Distribution string
	Error        string
}

type qaPlanPersonOption struct {
	qaPerson
	Checked bool
}

// readQARunForm reads the new-run form. Scenarios must be the plan's and
// testers must be people who can execute here; anything else is 404.
func readQARunForm(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, plan *repo.QAPlan) (repo.QARunInput, []qaPerson, error) {
	in := repo.QARunInput{Name: c.FormValue("name"), Notes: c.FormValue("notes"), Distribution: c.FormValue("distribution")}
	planScenarios, err := repo.ListQAPlanScenarioIDs(c.Context(), s, plan.ID)
	if err != nil {
		return in, nil, err
	}
	inPlan := map[string]bool{}
	for _, id := range planScenarios {
		inPlan[id.String()] = true
	}
	for _, raw := range c.Context().PostArgs().PeekMulti("scenario_id") {
		if !inPlan[string(raw)] {
			return in, nil, fiber.ErrNotFound
		}
		id, _ := uuid.Parse(string(raw))
		in.ScenarioIDs = append(in.ScenarioIDs, id)
	}
	people, err := qaPeople(c, d, s, project)
	if err != nil {
		return in, nil, err
	}
	ok := map[string]bool{}
	for _, p := range people {
		ok[p.ID.String()] = true
	}
	for _, raw := range c.Context().PostArgs().PeekMulti("tester_id") {
		if !ok[string(raw)] {
			return in, nil, fiber.ErrNotFound
		}
		id, _ := uuid.Parse(string(raw))
		in.Testers = append(in.Testers, id)
	}
	return in, people, nil
}

// qaRunErrMessage is the sentence for a refused run.
func qaRunErrMessage(err error, in repo.QARunInput) (int, string, bool) {
	switch {
	case errors.Is(err, repo.ErrQAClosed):
		return fiber.StatusConflict, "This plan is closed.", true
	case errors.Is(err, repo.ErrQAEmptyRun):
		return fiber.StatusUnprocessableEntity, "The run would have no cases.", true
	case errors.Is(err, repo.ErrInvalidInput) && len(in.Testers) == 0 && in.Distribution != "" && in.Distribution != repo.DistributeNone:
		return fiber.StatusUnprocessableEntity, "Choose at least one tester to share the cases between.", true
	case errors.Is(err, repo.ErrInvalidInput):
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Run name is required and can be at most %d characters.", repo.MaxQANameLength), true
	}
	return 0, "", false
}

func renderQARunNew(c *fiber.Ctx, d Deps, s tenant.Scope, data qaRunNewData, chosen map[uuid.UUID]bool, testers map[uuid.UUID]bool, status int) error {
	project := &data.Project
	ids, err := repo.ListQAPlanScenarioIDs(c.Context(), s, data.Plan.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	all, err := qaPlanScenarioOptions(c, s, project, nil)
	if err != nil {
		return writeRepoErr(c, err)
	}
	inPlan := map[uuid.UUID]bool{}
	for _, id := range ids {
		inPlan[id] = true
	}
	for _, o := range all {
		if inPlan[o.ID] {
			o.Checked = len(chosen) == 0 || chosen[o.ID]
			data.Scenarios = append(data.Scenarios, o)
		}
	}
	people, err := qaPeople(c, d, s, project)
	if err != nil {
		return writeRepoErr(c, err)
	}
	for _, p := range people {
		data.People = append(data.People, qaPlanPersonOption{qaPerson: p, Checked: testers[p.ID]})
	}
	org, user, err := shellChrome(c, d.DB)
	if err != nil {
		return err
	}
	nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Plans", Href: data.Base + "/plans"},
		httpx.NavCrumb{Label: data.Plan.Name, Href: planURL(project, &data.Plan)}, httpx.NavCrumb{Label: "New run"})
	if err != nil {
		return writeRepoErr(c, err)
	}
	c.Type("html")
	c.Status(status)
	return renderPage(c, d, "pages/qa_run_new.html", httpx.ViewData{
		Title: "New run · " + data.Plan.Name, Org: org, User: user, Nav: nav, Data: data,
	})
}

func newQARunPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		runs, err := repo.ListQARuns(c.Context(), s, plan.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data := qaRunNewData{Project: *project, Base: qaBase(project), Plan: *plan,
			Name: fmt.Sprintf("Cycle %d", len(runs)+1), Distribution: repo.DistributeNone}
		if plan.ClosedAt != nil {
			data.Error = "This plan is closed."
		}
		return renderQARunNew(c, d, s, data, nil, nil, fiber.StatusOK)
	}
}

func createQARunUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		in, _, err := readQARunForm(c, d, s, project, plan)
		if err != nil {
			return err
		}
		run, err := repo.CreateQARun(c.Context(), s, plan.ID, in, qaActor(c))
		if err != nil {
			status, msg, ok := qaRunErrMessage(err, in)
			if !ok {
				return writeRepoErr(c, err)
			}
			data := qaRunNewData{Project: *project, Base: qaBase(project), Plan: *plan, Name: in.Name, Notes: in.Notes,
				Distribution: in.Distribution, Error: msg}
			chosen, testers := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
			for _, id := range in.ScenarioIDs {
				chosen[id] = true
			}
			for _, id := range in.Testers {
				testers[id] = true
			}
			return renderQARunNew(c, d, s, data, chosen, testers, status)
		}
		setFlash(c, "qa.run.created", run.Name)
		return c.Redirect(qaBase(project)+"/runs/"+run.ID.String(), fiber.StatusFound)
	}
}

type qaRunPreviewRow struct {
	Label string
	N     int
}

type qaRunPreviewData struct {
	Cases      int
	Rows       []qaRunPreviewRow
	Unassigned int
	Error      string
}

// previewQARunUI is the new-run form's live preview (an htmx fragment):
// how many cases, and how many each chosen tester would get.
func previewQARunUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		in, people, err := readQARunForm(c, d, s, project, plan)
		if err != nil {
			return err
		}
		var data qaRunPreviewData
		p, err := repo.PreviewQARun(c.Context(), s, plan.ID, in)
		if err != nil {
			_, msg, ok := qaRunErrMessage(err, in)
			if !ok {
				return writeRepoErr(c, err)
			}
			data.Error = msg
		} else {
			data.Cases, data.Unassigned = p.Cases, p.Unassigned
			if p.Cases == 0 {
				data.Error = "The run would have no cases."
			}
			for _, person := range people {
				if n, ok := p.PerTester[person.ID]; ok {
					data.Rows = append(data.Rows, qaRunPreviewRow{Label: person.Label, N: n})
				}
			}
		}
		c.Type("html")
		return d.Renderer.RenderNamed(c, "qa_run_preview", data)
	}
}
