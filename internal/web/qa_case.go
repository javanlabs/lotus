package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

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
	PicID          string // the case's own PIC, "" for none (its section's applies)
	Error          string
	CanWrite       bool
	SectionOptions []httpx.SelectOption
	PriorityOpts   []httpx.SelectOption
	SectionPath    string
	// SectionArchived: the case is filed in an archived section. It is
	// offered in the picker (marked) so the box never shows a bare id, and
	// the case cannot be restored until it moves to a live section.
	SectionArchived bool
	PICOptions      []httpx.SelectOption
	PIC             *qaPICView // effective: own, else inherited from a section
	CreatedBy       string     // the author, named; "" when unknown
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
// sentence to show; an unknown or foreign section is 404. The PIC must be
// someone who can be one here, or the case's current PIC.
func readQACaseForm(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, f *qaCaseForm) (repo.QACaseInput, error) {
	var keep *uuid.UUID
	if f.Case != nil {
		keep = f.Case.PicID
	}
	f.Title = c.FormValue("title")
	f.SectionID = c.FormValue("section_id")
	f.Priority = strings.TrimSpace(c.FormValue("priority"))
	f.Negative = c.FormValue("negative") != ""
	f.Precondition = c.FormValue("precondition")
	f.Notes = c.FormValue("notes")
	f.Steps = qaStepsFromForm(c)
	f.PicID = strings.TrimSpace(c.FormValue("pic_id"))
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
	if f.PicID != "" {
		people, err := qaPeople(c, d, s, project)
		if err != nil {
			return in, err
		}
		if in.PicID, err = qaPICFrom(people, f.PicID, keep); errors.Is(err, errQAPIC) {
			f.Error = qaPICErrMessage
		} else if err != nil {
			return in, err
		}
	}
	return in, nil
}

func qaCaseErrMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, repo.ErrInvalidInput):
		return fmt.Sprintf("Title is required and can be at most %d characters.", repo.MaxQATitleLength), true
	case errors.Is(err, repo.ErrQASectionArchived):
		return "That section is archived. Choose another.", true
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
	if err := fillQACasePIC(c, d, s, &f, sections); err != nil {
		return writeRepoErr(c, err)
	}
	for _, r := range tree {
		if r.ID.String() == f.SectionID {
			f.SectionPath = r.Path
		}
	}
	if f.SectionPath == "" && f.Case != nil { // the case sits in an archived section
		for _, r := range qaSectionTree(sections, nil, true, nil) {
			if r.ID == f.Case.SectionID {
				f.SectionPath, f.SectionArchived = r.Path, true
				f.SectionOptions = append(f.SectionOptions,
					httpx.SelectOption{Value: r.ID.String(), Label: r.Path + " (archived)"})
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

// fillQACasePIC names the case's PIC (its own, else its section's) and
// author, and lists who can be picked as PIC.
func fillQACasePIC(c *fiber.Ctx, d Deps, s tenant.Scope, f *qaCaseForm, sections []repo.QASection) error {
	var own, author *uuid.UUID
	if id, err := uuid.Parse(f.PicID); err == nil {
		own = &id
	}
	if f.Case != nil {
		author = f.Case.CreatedBy
	}
	labels, err := qaUserLabels(c, d, qaPICIDs(sections, own, author))
	if err != nil {
		return err
	}
	if author != nil {
		f.CreatedBy = personLabel(labels, author)
	}
	sectionID, _ := uuid.Parse(f.SectionID)
	f.PIC = qaEffectivePICView(sections, sectionID, own, labels)
	if !f.CanWrite {
		return nil
	}
	people, err := qaPeople(c, d, s, &f.Project)
	if err != nil {
		return err
	}
	none := "Nobody"
	if inherited := qaEffectivePICView(sections, sectionID, nil, labels); inherited != nil {
		none = "Section's PIC (" + inherited.Label + ")"
	}
	f.PICOptions = qaPICOptions(people, none, own, labels)
	return nil
}

func formFromCase(project *repo.Project, qc *repo.QACase) qaCaseForm {
	f := qaCaseForm{
		Project: *project, Base: qaBase(project), Case: qc, Title: qc.Title, SectionID: qc.SectionID.String(),
		Negative: qc.Negative, Precondition: qc.Precondition, Notes: qc.Notes, Steps: qc.Steps,
	}
	if qc.Priority != nil {
		f.Priority = *qc.Priority
	}
	f.PicID = uuidString(qc.PicID)
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
		in, err := readQACaseForm(c, d, s, project, &f)
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
		in, err := readQACaseForm(c, d, s, project, &f)
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
		if archive && qc.ArchivedAt != nil { // already archived: nothing to do, nothing to audit
			return c.Redirect(fmt.Sprintf("%s/cases/%d", qaBase(project), qc.Key), fiber.StatusFound)
		}
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
				if errors.Is(err, repo.ErrQASectionArchived) {
					return fiber.NewError(fiber.StatusConflict, "Its section is archived. Move the case to a live section first.")
				}
				return writeRepoErr(c, err)
			}
			setFlash(c, "qa.case.restored", label)
		}
		return c.Redirect(fmt.Sprintf("%s/cases/%d", qaBase(project), qc.Key), fiber.StatusFound)
	}
}
