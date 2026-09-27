package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// qaPhaseLabels are the phases as people write them; the stored values
// stay lowercase.
var qaPhaseLabels = map[string]string{
	"smoke": "Smoke", "sit": "SIT", "uat": "UAT", "regression": "Regression", "other": "Other",
}

func qaPhaseOptions() []httpx.SelectOption {
	out := make([]httpx.SelectOption, 0, len(repo.QAPhases))
	for _, p := range repo.QAPhases {
		out = append(out, httpx.SelectOption{Value: p, Label: qaPhaseLabels[p]})
	}
	return out
}

type qaScenarioRow struct {
	repo.QAScenario
	PhaseLabel string
	Count      repo.QAScenarioCount
	Href       string
}

type qaScenariosData struct {
	Project      repo.Project
	Base         string
	Rows         []qaScenarioRow
	Archived     bool
	CanWrite     bool
	PhaseOptions []httpx.SelectOption
}

func qaScenariosPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		data := qaScenariosData{
			Project: *project, Base: qaBase(project), Archived: c.Query("archived") == "1",
			CanWrite: mustPrincipal(c).Can("qa.case.write"), PhaseOptions: qaPhaseOptions(),
		}
		list, err := repo.ListQAScenarios(c.Context(), s, project.ID, data.Archived)
		if err != nil {
			return writeRepoErr(c, err)
		}
		counts, err := repo.QAScenarioCaseCounts(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, sc := range list {
			data.Rows = append(data.Rows, qaScenarioRow{
				QAScenario: sc, PhaseLabel: qaPhaseLabels[sc.Phase], Count: counts[sc.ID],
				Href: data.Base + "/scenarios/" + sc.ID.String(),
			})
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Scenarios"})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_scenarios.html", httpx.ViewData{
			Title: "Scenarios · " + project.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

// readQAScenarioForm reads name, phase and description. msg is the sentence
// to show when the form is rejected.
func readQAScenarioForm(c *fiber.Ctx) (name, phase, description, msg string) {
	name, phase, description = c.FormValue("name"), strings.TrimSpace(c.FormValue("phase")), c.FormValue("description")
	if _, ok := qaPhaseLabels[phase]; !ok {
		return name, phase, description, "Choose a phase."
	}
	return name, phase, description, ""
}

func qaScenarioErr(err error) (int, string, bool) {
	if errors.Is(err, repo.ErrInvalidInput) {
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Scenario name is required and can be at most %d characters.", repo.MaxQANameLength), true
	}
	return 0, "", false
}

func createQAScenarioUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		name, phase, description, msg := readQAScenarioForm(c)
		if msg != "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, msg)
		}
		sc, err := repo.CreateQAScenario(c.Context(), s, project.ID, name, phase, description, qaActor(c))
		if err != nil {
			if status, msg, ok := qaScenarioErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.scenario.created", sc.Name)
		return redirectAfterCreate(c, qaBase(project)+"/scenarios/"+sc.ID.String())
	}
}

// loadQAScenario reads :sid, which must be a scenario of the project in the URL.
func loadQAScenario(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (*repo.QAScenario, error) {
	id, err := qaOwned(c, s, project, repo.QAKindScenario, c.Params("sid"))
	if err != nil {
		return nil, err
	}
	return repo.GetQAScenario(c.Context(), s, id)
}

func updateQAScenarioUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, sc, ok, err := writableQAScenario(c, d)
		if !ok {
			return err
		}
		name, phase, description, msg := readQAScenarioForm(c)
		if msg != "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, msg)
		}
		if err := repo.UpdateQAScenario(c.Context(), s, sc.ID, name, phase, description); err != nil {
			if status, msg, ok := qaScenarioErr(err); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.scenario.saved", strings.TrimSpace(name))
		return redirectAfterMutation(c, qaBase(project)+"/scenarios/"+sc.ID.String())
	}
}

func archiveQAScenarioUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sc, err := loadQAScenario(c, s, project)
		if err != nil {
			return err
		}
		if sc.ArchivedAt != nil { // already archived: nothing to do, nothing to audit
			return redirectAfterMutation(c, qaBase(project)+"/scenarios")
		}
		if err := repo.ArchiveQAScenario(c.Context(), s, sc.ID); err != nil {
			return writeRepoErr(c, err)
		}
		raw, _ := json.Marshal(fiber.Map{"name": sc.Name, "project_id": project.ID})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.scenario.archive", "qa_scenario", &sc.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.scenario.archived", sc.Name)
		return redirectAfterMutation(c, qaBase(project)+"/scenarios")
	}
}
