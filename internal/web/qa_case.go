package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

var qaPriorityOptions = []httpx.SelectOption{
	{Value: "", Label: "—"}, {Value: "high", Label: "high"}, {Value: "medium", Label: "medium"}, {Value: "low", Label: "low"},
}

// qaCaseForm is the editor's state: what was loaded or what was typed, so a
// rejected save shows the form again exactly as submitted.
type qaCaseForm struct {
	Project        repo.Project
	Base           string
	Case           *repo.QACase // nil while creating
	Title          string
	SectionID      string
	Priority       string
	Negative       bool
	Precondition   string
	Notes          string
	Steps          repo.QASteps
	Error          string
	CanWrite       bool
	SectionOptions []httpx.SelectOption
	PriorityOpts   []httpx.SelectOption
	SectionPath    string
}

// Action is where the form posts.
func (f qaCaseForm) Action() string {
	if f.Case == nil {
		return f.Base + "/cases/new"
	}
	return fmt.Sprintf("%s/cases/%d", f.Base, f.Case.Key)
}

// qaStepsFromForm pairs step_action[i] with step_expected[i]. A pair with
// both halves blank is an empty editor row and is dropped.
func qaStepsFromForm(c *fiber.Ctx) repo.QASteps {
	actions := c.Context().PostArgs().PeekMulti("step_action")
	expected := c.Context().PostArgs().PeekMulti("step_expected")
	steps := repo.QASteps{}
	for i := 0; i < max(len(actions), len(expected)); i++ {
		var a, x string
		if i < len(actions) {
			a = strings.TrimSpace(string(actions[i]))
		}
		if i < len(expected) {
			x = strings.TrimSpace(string(expected[i]))
		}
		if a == "" && x == "" {
			continue
		}
		steps = append(steps, repo.QAStep{Action: a, Expected: x})
	}
	return steps
}

// readQACaseForm reads a submitted case. It returns the repo input, or the
// sentence to show; an unknown or foreign section is 404.
func readQACaseForm(c *fiber.Ctx, s tenant.Scope, project *repo.Project, f *qaCaseForm) (repo.QACaseInput, error) {
	f.Title = c.FormValue("title")
	f.SectionID = c.FormValue("section_id")
	f.Priority = strings.TrimSpace(c.FormValue("priority"))
	f.Negative = c.FormValue("negative") != ""
	f.Precondition = c.FormValue("precondition")
	f.Notes = c.FormValue("notes")
	f.Steps = qaStepsFromForm(c)
	if strings.TrimSpace(f.SectionID) == "" {
		f.Error = "Choose a section."
		return repo.QACaseInput{}, nil
	}
	sectionID, err := qaOwned(c, s, project, repo.QAKindSection, f.SectionID)
	if err != nil {
		return repo.QACaseInput{}, err
	}
	in := repo.QACaseInput{
		SectionID: sectionID, Title: f.Title, Precondition: f.Precondition, Notes: f.Notes,
		Steps: f.Steps, Negative: f.Negative,
	}
	switch f.Priority {
	case "":
	case "high", "medium", "low":
		p := f.Priority
		in.Priority = &p
	default:
		f.Error = "Choose a priority: high, medium or low."
	}
	return in, nil
}

func qaCaseErrMessage(err error) (string, bool) {
	if errors.Is(err, repo.ErrInvalidInput) {
		return fmt.Sprintf("Title is required and can be at most %d characters.", repo.MaxQATitleLength), true
	}
	return "", false
}

// renderQACase shows the editor (or, without qa.case.write, the read-only
// case).
func renderQACase(c *fiber.Ctx, d Deps, s tenant.Scope, f qaCaseForm, status int) error {
	sections, err := repo.ListQASections(c.Context(), s, f.Project.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	tree := qaSectionTree(sections, nil, false, nil)
	f.SectionOptions = qaSectionOptions(tree, nil)
	f.PriorityOpts = qaPriorityOptions
	for _, r := range tree {
		if r.ID.String() == f.SectionID {
			f.SectionPath = r.Path
		}
	}
	if f.SectionPath == "" && f.Case != nil { // the case sits in an archived section
		for _, r := range qaSectionTree(sections, nil, true, nil) {
			if r.ID == f.Case.SectionID {
				f.SectionPath = r.Path
			}
		}
	}
	org, user, err := shellChrome(c, d.DB)
	if err != nil {
		return err
	}
	label := "New case"
	if f.Case != nil {
		label = fmt.Sprintf("TC-%d", f.Case.Key)
	}
	nav, err := qaNav(c, s, &f.Project, httpx.NavCrumb{Label: "Cases", Href: f.Base + "/cases"}, httpx.NavCrumb{Label: label})
	if err != nil {
		return writeRepoErr(c, err)
	}
	c.Type("html")
	c.Status(status)
	return renderPage(c, d, "pages/qa_case.html", httpx.ViewData{
		Title: label + " · " + f.Project.Name, Org: org, User: user, Nav: nav, Data: f,
	})
}

func formFromCase(project *repo.Project, qc *repo.QACase) qaCaseForm {
	f := qaCaseForm{
		Project: *project, Base: qaBase(project), Case: qc, Title: qc.Title, SectionID: qc.SectionID.String(),
		Negative: qc.Negative, Precondition: qc.Precondition, Notes: qc.Notes, Steps: qc.Steps,
	}
	if qc.Priority != nil {
		f.Priority = *qc.Priority
	}
	return f
}

// loadQACase reads :key within the project in the URL.
func loadQACase(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (*repo.QACase, error) {
	key, err := strconv.Atoi(c.Params("key"))
	if err != nil || key < 1 {
		return nil, fiber.ErrNotFound
	}
	qc, err := repo.GetQACaseByKey(c.Context(), s, project.ID, key)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, fiber.ErrNotFound
	}
	return qc, err
}

func newQACasePage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		f := qaCaseForm{Project: *project, Base: qaBase(project), CanWrite: true, Steps: repo.QASteps{}}
		if raw := c.Query("section"); raw != "" {
			if _, err := qaOwned(c, s, project, repo.QAKindSection, raw); err == nil {
				f.SectionID = raw
			}
		}
		return renderQACase(c, d, s, f, fiber.StatusOK)
	}
}

func createQACaseUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		f := qaCaseForm{Project: *project, Base: qaBase(project), CanWrite: true}
		in, err := readQACaseForm(c, s, project, &f)
		if err != nil {
			return err
		}
		if f.Error != "" {
			return renderQACase(c, d, s, f, fiber.StatusUnprocessableEntity)
		}
		qc, err := repo.CreateQACase(c.Context(), s, project.ID, in, qaActor(c))
		if err != nil {
			if msg, ok := qaCaseErrMessage(err); ok {
				f.Error = msg
				return renderQACase(c, d, s, f, fiber.StatusUnprocessableEntity)
			}
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.case.created", fmt.Sprintf("TC-%d", qc.Key))
		return c.Redirect(fmt.Sprintf("%s/cases/%d", f.Base, qc.Key), fiber.StatusFound)
	}
}

func qaCasePage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		qc, err := loadQACase(c, s, project)
		if err != nil {
			return err
		}
		f := formFromCase(project, qc)
		f.CanWrite = mustPrincipal(c).Can("qa.case.write")
		return renderQACase(c, d, s, f, fiber.StatusOK)
	}
}

func updateQACaseUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		qc, err := loadQACase(c, s, project)
		if err != nil {
			return err
		}
		f := formFromCase(project, qc)
		f.CanWrite = true
		in, err := readQACaseForm(c, s, project, &f)
		if err != nil {
			return err
		}
		if f.Error != "" {
			return renderQACase(c, d, s, f, fiber.StatusUnprocessableEntity)
		}
		if _, err := repo.UpdateQACase(c.Context(), s, qc.ID, in); err != nil {
			if msg, ok := qaCaseErrMessage(err); ok {
				f.Error = msg
				return renderQACase(c, d, s, f, fiber.StatusUnprocessableEntity)
			}
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.case.saved", fmt.Sprintf("TC-%d", qc.Key))
		return c.Redirect(f.Action(), fiber.StatusFound)
	}
}

// setQACaseArchivedUI archives or restores :key. Archiving is audited;
// runs already holding the case keep their copy of it.
func setQACaseArchivedUI(archive bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		qc, err := loadQACase(c, s, project)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("TC-%d", qc.Key)
		if archive {
			if err := repo.ArchiveQACase(c.Context(), s, qc.ID); err != nil {
				return writeRepoErr(c, err)
			}
			raw, _ := json.Marshal(fiber.Map{"key": qc.Key, "title": qc.Title, "project_id": project.ID})
			if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.case.archive", "qa_case", &qc.ID, raw); err != nil {
				return writeRepoErr(c, err)
			}
			setFlash(c, "qa.case.archived", label)
		} else {
			if err := repo.RestoreQACase(c.Context(), s, qc.ID); err != nil {
				return writeRepoErr(c, err)
			}
			setFlash(c, "qa.case.restored", label)
		}
		return c.Redirect(fmt.Sprintf("%s/cases/%d", qaBase(project), qc.Key), fiber.StatusFound)
	}
}
