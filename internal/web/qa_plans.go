package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaWindow is a plan's date window as people read it: "01 Sep – 14 Sep
// 2026", "from 01 Sep 2026", "" when there is none.
func qaWindow(start, end *time.Time) string {
	switch {
	case start != nil && end != nil && start.Year() == end.Year():
		return start.Format("02 Jan") + " – " + end.Format("02 Jan 2006")
	case start != nil && end != nil:
		return start.Format("02 Jan 2006") + " – " + end.Format("02 Jan 2006")
	case start != nil:
		return "from " + start.Format("02 Jan 2006")
	case end != nil:
		return "until " + end.Format("02 Jan 2006")
	}
	return ""
}

func dateValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// qaPlanFormData fills the New plan / Edit plan form.
type qaPlanFormData struct {
	Action, Err, Submit      string
	Name, Phase, Environment string
	Build, StartsOn, EndsOn  string
	PhaseOptions             []httpx.SelectOption
	Scenarios                []qaPlanScenarioOption
}

type qaPlanScenarioOption struct {
	ID         uuid.UUID
	Name       string
	PhaseLabel string
	Checked    bool
}

// qaPlanScenarioOptions lists the project's live scenarios for the form,
// ticking the ones in chosen.
func qaPlanScenarioOptions(c *fiber.Ctx, s tenant.Scope, project *repo.Project, chosen []uuid.UUID) ([]qaPlanScenarioOption, error) {
	list, err := repo.ListQAScenarios(c.Context(), s, project.ID, false)
	if err != nil {
		return nil, err
	}
	on := map[uuid.UUID]bool{}
	for _, id := range chosen {
		on[id] = true
	}
	out := make([]qaPlanScenarioOption, 0, len(list))
	for _, sc := range list {
		out = append(out, qaPlanScenarioOption{ID: sc.ID, Name: sc.Name, PhaseLabel: qaPhaseLabels[sc.Phase], Checked: on[sc.ID]})
	}
	return out, nil
}

// readQAPlanForm reads a submitted plan. msg is the sentence to show for a
// rejected form; a scenario of another project is 404.
func readQAPlanForm(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (repo.QAPlanInput, string, error) {
	in := repo.QAPlanInput{
		Name: c.FormValue("name"), Phase: strings.TrimSpace(c.FormValue("phase")),
		Environment: c.FormValue("environment"), Build: c.FormValue("build"),
	}
	if _, ok := qaPhaseLabels[in.Phase]; !ok {
		return in, "Choose a phase.", nil
	}
	for _, f := range []struct {
		field string
		to    **time.Time
	}{{"starts_on", &in.StartsOn}, {"ends_on", &in.EndsOn}} {
		raw := strings.TrimSpace(c.FormValue(f.field))
		if raw == "" {
			continue
		}
		d, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return in, "Dates are written as YYYY-MM-DD.", nil
		}
		*f.to = &d
	}
	if in.StartsOn != nil && in.EndsOn != nil && in.EndsOn.Before(*in.StartsOn) {
		return in, "The end date is before the start date.", nil
	}
	for _, raw := range c.Context().PostArgs().PeekMulti("scenario_id") {
		id, err := qaOwned(c, s, project, repo.QAKindScenario, string(raw))
		if err != nil {
			return in, "", err
		}
		in.ScenarioIDs = append(in.ScenarioIDs, id)
	}
	return in, "", nil
}

func qaPlanNameErr(err error) (int, string, bool) {
	if errors.Is(err, repo.ErrInvalidInput) {
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Plan name is required and can be at most %d characters.", repo.MaxQANameLength), true
	}
	return 0, "", false
}

// loadQAPlan reads :pid, which must be a plan of the project in the URL.
func loadQAPlan(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (*repo.QAPlan, error) {
	id, err := qaOwned(c, s, project, repo.QAKindPlan, c.Params("pid"))
	if err != nil {
		return nil, err
	}
	return repo.GetQAPlan(c.Context(), s, id)
}

func planURL(project *repo.Project, p *repo.QAPlan) string {
	return qaBase(project) + "/plans/" + p.ID.String()
}

type qaPlanRow struct {
	repo.QAPlan
	PhaseLabel string
	Window     string
	Runs       int
	Progress   repo.QAProgress
	Href       string
}

type qaPlansData struct {
	Project  repo.Project
	Base     string
	Rows     []qaPlanRow
	Phase    string
	CanWrite bool
	Form     qaPlanFormData
	Filters  []httpx.SelectOption
}

func qaPlansPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		data := qaPlansData{Project: *project, Base: qaBase(project), Phase: c.Query("phase"),
			CanWrite: mustPrincipal(c).Can("qa.plan.write")}
		if _, ok := qaPhaseLabels[data.Phase]; !ok {
			data.Phase = ""
		}
		data.Filters = append([]httpx.SelectOption{{Value: "", Label: "All phases"}}, qaPhaseOptions()...)
		plans, err := repo.ListQAPlans(c.Context(), s, project.ID) // open first, newest first
		if err != nil {
			return writeRepoErr(c, err)
		}
		ids := make([]uuid.UUID, 0, len(plans))
		for _, p := range plans {
			if data.Phase == "" || p.Phase == data.Phase {
				ids = append(ids, p.ID)
			}
		}
		progress, err := repo.QAPlanProgress(c.Context(), s, ids)
		if err != nil {
			return writeRepoErr(c, err)
		}
		runs, err := repo.QAPlanRunCounts(c.Context(), s, ids)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, p := range plans {
			if data.Phase != "" && p.Phase != data.Phase {
				continue
			}
			data.Rows = append(data.Rows, qaPlanRow{QAPlan: p, PhaseLabel: qaPhaseLabels[p.Phase], Window: qaWindow(p.StartsOn, p.EndsOn),
				Runs: runs[p.ID], Progress: progress[p.ID], Href: data.Base + "/plans/" + p.ID.String()})
		}
		if data.CanWrite {
			scenarios, err := qaPlanScenarioOptions(c, s, project, nil)
			if err != nil {
				return writeRepoErr(c, err)
			}
			data.Form = qaPlanFormData{Action: data.Base + "/plans", Err: "new-plan-error", Submit: "Create plan",
				Phase: "regression", PhaseOptions: qaPhaseOptions(), Scenarios: scenarios}
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Plans"})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_plans.html", httpx.ViewData{
			Title: "Plans · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

func createQAPlanUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		in, msg, err := readQAPlanForm(c, s, project)
		if err != nil {
			return err
		}
		if msg != "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, msg)
		}
		plan, err := repo.CreateQAPlan(c.Context(), s, project.ID, in, qaActor(c))
		if err != nil {
			if status, msg, ok := qaPlanNameErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.plan.created", plan.Name)
		return redirectAfterCreate(c, planURL(project, plan))
	}
}

type qaPlanRunRow struct {
	repo.QARun
	Href       string
	RetestOf   string // the name of the run this retests, "" otherwise
	RetestHref string
	Progress   repo.QAProgress
}

type qaPlanData struct {
	Project    repo.Project
	Base       string
	Plan       repo.QAPlan
	PhaseLabel string
	Window     string
	Scenarios  []qaScenarioRow
	Runs       []qaPlanRunRow
	Progress   repo.QAProgress
	CanWrite   bool // qa.plan.write on an open plan
	CanCopy    bool // qa.plan.write (a closed plan can still be duplicated)
	Form       qaPlanFormData
	CopyName   string
}

func qaPlanPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		canWrite := mustPrincipal(c).Can("qa.plan.write")
		data := qaPlanData{Project: *project, Base: qaBase(project), Plan: *plan, PhaseLabel: qaPhaseLabels[plan.Phase],
			Window: qaWindow(plan.StartsOn, plan.EndsOn), CanWrite: canWrite && plan.ClosedAt == nil, CanCopy: canWrite,
			CopyName: plan.Name + " (copy)"}
		scenarioIDs, err := repo.ListQAPlanScenarioIDs(c.Context(), s, plan.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		inPlan := map[uuid.UUID]bool{}
		for _, id := range scenarioIDs {
			inPlan[id] = true
		}
		all, err := repo.ListQAScenarios(c.Context(), s, project.ID, true)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, sc := range all {
			if inPlan[sc.ID] {
				data.Scenarios = append(data.Scenarios, qaScenarioRow{QAScenario: sc, PhaseLabel: qaPhaseLabels[sc.Phase],
					Href: data.Base + "/scenarios/" + sc.ID.String()})
			}
		}
		runs, err := repo.ListQARuns(c.Context(), s, plan.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		runIDs := make([]uuid.UUID, len(runs))
		names := map[uuid.UUID]string{}
		for i, r := range runs {
			runIDs[i], names[r.ID] = r.ID, r.Name
		}
		progress, err := repo.QARunProgressFor(c.Context(), s, runIDs)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, r := range runs {
			row := qaPlanRunRow{QARun: r, Href: data.Base + "/runs/" + r.ID.String(), Progress: progress[r.ID]}
			if r.RetestOf != nil {
				row.RetestOf, row.RetestHref = names[*r.RetestOf], data.Base+"/runs/"+r.RetestOf.String()
			}
			data.Runs = append(data.Runs, row)
		}
		planProgress, err := repo.QAPlanProgress(c.Context(), s, []uuid.UUID{plan.ID})
		if err != nil {
			return writeRepoErr(c, err)
		}
		data.Progress = planProgress[plan.ID]
		if data.CanWrite {
			options, err := qaPlanScenarioOptions(c, s, project, scenarioIDs)
			if err != nil {
				return writeRepoErr(c, err)
			}
			data.Form = qaPlanFormData{Action: planURL(project, plan), Err: "edit-plan-error", Submit: "Save",
				Name: plan.Name, Phase: plan.Phase, Environment: plan.Environment, Build: plan.Build,
				StartsOn: dateValue(plan.StartsOn), EndsOn: dateValue(plan.EndsOn),
				PhaseOptions: qaPhaseOptions(), Scenarios: options}
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Plans", Href: data.Base + "/plans"}, httpx.NavCrumb{Label: plan.Name})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_plan.html", httpx.ViewData{
			Title: plan.Name + " · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

func updateQAPlanUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		if plan.ClosedAt != nil {
			return qaFormError(c, d, fiber.StatusConflict, "This plan is closed.")
		}
		in, msg, err := readQAPlanForm(c, s, project)
		if err != nil {
			return err
		}
		if msg != "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, msg)
		}
		if err := repo.UpdateQAPlan(c.Context(), s, plan.ID, in); err != nil {
			if status, msg, ok := qaPlanNameErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.plan.saved", strings.TrimSpace(in.Name))
		return redirectAfterMutation(c, planURL(project, plan))
	}
}

// duplicateQAPlanUI starts the next cycle of work from a plan: the same
// phase, environment, build and scenarios, a new name, no dates.
func duplicateQAPlanUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		dup, err := repo.DuplicateQAPlan(c.Context(), s, plan.ID, c.FormValue("name"), qaActor(c))
		if err != nil {
			if status, msg, ok := qaPlanNameErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.plan.created", dup.Name)
		return redirectAfterCreate(c, planURL(project, dup))
	}
}

func closeQAPlanUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		if plan.ClosedAt != nil { // already closed: nothing to do, nothing to audit
			return redirectAfterMutation(c, planURL(project, plan))
		}
		if err := repo.CloseQAPlan(c.Context(), s, plan.ID); err != nil {
			return writeRepoErr(c, err)
		}
		raw, _ := json.Marshal(fiber.Map{"name": plan.Name, "project_id": project.ID})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.plan.close", "qa_plan", &plan.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.plan.closed", plan.Name)
		return redirectAfterMutation(c, planURL(project, plan))
	}
}
