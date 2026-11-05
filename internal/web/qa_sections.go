package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Section routes follow the app's form conventions (forms.go): form posts, a
// rejection inside the still-open dialog for htmx, a success with a flash
// and redirectAfterMutation. Only archiving is audited — it takes every
// case in the subtree with it; creating, renaming and moving are routine.

// qaSectionErr turns a repo error into the status and sentence a person sees.
func qaSectionErr(err error, name string) (int, string, bool) {
	switch {
	case errors.Is(err, repo.ErrQANameTaken):
		return fiber.StatusConflict, fmt.Sprintf("There's already a section called '%s' here.", name), true
	case errors.Is(err, repo.ErrQASectionCycle):
		return fiber.StatusConflict, "Can't move a section into itself or its own subsection.", true
	case errors.Is(err, repo.ErrQASectionArchived):
		return fiber.StatusConflict, "That section is archived.", true
	case errors.Is(err, repo.ErrQASectionTooDeep):
		return fiber.StatusConflict, fmt.Sprintf("Sections can nest at most %d levels.", repo.MaxQASectionDepth), true
	case errors.Is(err, repo.ErrInvalidInput):
		return fiber.StatusUnprocessableEntity,
			fmt.Sprintf("Section name is required and can be at most %d characters.", repo.MaxQANameLength), true
	case errors.Is(err, repo.ErrNotFound):
		return fiber.StatusNotFound, "That section no longer exists.", true
	}
	return 0, "", false
}

// qaOptionalSection reads a parent picker: "" is the top level (nil); any
// other value must be a section of this project.
func qaOptionalSection(c *fiber.Ctx, s tenant.Scope, project *repo.Project, raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := qaOwned(c, s, project, repo.QAKindSection, raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// qaSection loads :sid, which must belong to the project in the URL.
func qaSection(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (repo.QASection, error) {
	id, err := qaOwned(c, s, project, repo.QAKindSection, c.Params("sid"))
	if err != nil {
		return repo.QASection{}, err
	}
	secs, err := repo.ListQASections(c.Context(), s, project.ID)
	if err != nil {
		return repo.QASection{}, err
	}
	for _, sec := range secs {
		if sec.ID == id {
			return sec, nil
		}
	}
	return repo.QASection{}, fiber.ErrNotFound
}

// qaSectionPICFromForm reads a section dialog's PIC picker (errQAPIC for
// someone who cannot be PIC; keep is the section's current PIC).
func qaSectionPICFromForm(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, keep *uuid.UUID) (*uuid.UUID, error) {
	raw := c.FormValue("pic_id")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	people, err := qaPeople(c, d, s, project)
	if err != nil {
		return nil, err
	}
	return qaPICFrom(people, raw, keep)
}

func createQASectionUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		parentID, err := qaOptionalSection(c, s, project, c.FormValue("parent_id"))
		if err != nil {
			return err
		}
		pic, err := qaSectionPICFromForm(c, d, s, project, nil)
		if errors.Is(err, errQAPIC) {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, qaPICErrMessage)
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		name := c.FormValue("name")
		sec, err := repo.CreateQASection(c.Context(), s, project.ID, name, parentID)
		if err != nil {
			if status, msg, ok := qaSectionErr(err, strings.TrimSpace(name)); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		if pic != nil {
			if err := repo.SetQASectionPIC(c.Context(), s, sec.ID, pic); err != nil {
				return writeRepoErr(c, err)
			}
		}
		setFlash(c, "qa.section.created", sec.Name)
		return redirectAfterCreate(c, casesURL(qaBase(project), sec.ID.String(), "", false, 1))
	}
}

// renameQASectionUI saves the edit dialog's name and, when the form sends
// pic_id at all, the section's PIC.
func renameQASectionUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sec, err := qaSection(c, s, project)
		if err != nil {
			return err
		}
		setPIC := c.Request().PostArgs().Has("pic_id")
		var pic *uuid.UUID
		if setPIC {
			pic, err = qaSectionPICFromForm(c, d, s, project, sec.PicID)
			if errors.Is(err, errQAPIC) {
				return qaFormError(c, d, fiber.StatusUnprocessableEntity, qaPICErrMessage)
			}
			if err != nil {
				return writeRepoErr(c, err)
			}
		}
		name := strings.TrimSpace(c.FormValue("name"))
		if err := repo.RenameQASection(c.Context(), s, sec.ID, name); err != nil {
			if status, msg, ok := qaSectionErr(err, name); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		if setPIC {
			if err := repo.SetQASectionPIC(c.Context(), s, sec.ID, pic); err != nil {
				return writeRepoErr(c, err)
			}
		}
		setFlash(c, "qa.section.renamed", name)
		return redirectAfterMutation(c, casesURL(qaBase(project), sec.ID.String(), "", false, 1))
	}
}

func moveQASectionUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sec, err := qaSection(c, s, project)
		if err != nil {
			return err
		}
		parentID, err := qaOptionalSection(c, s, project, c.FormValue("parent_id"))
		if err != nil {
			return err
		}
		if err := repo.MoveQASection(c.Context(), s, sec.ID, parentID); err != nil {
			if status, msg, ok := qaSectionErr(err, sec.Name); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		setFlash(c, "qa.section.moved", sec.Name)
		return redirectAfterMutation(c, casesURL(qaBase(project), sec.ID.String(), "", false, 1))
	}
}

func archiveQASectionUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sec, err := qaSection(c, s, project)
		if err != nil {
			return err
		}
		if err := repo.ArchiveQASection(c.Context(), s, sec.ID); err != nil {
			if status, msg, ok := qaSectionErr(err, sec.Name); ok {
				return qaFormError(c, d, status, msg)
			}
			return err
		}
		raw, _ := json.Marshal(fiber.Map{"name": sec.Name, "project_id": project.ID})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.section.archive", "qa_section", &sec.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.section.archived", sec.Name)
		// Not redirectAfterMutation: HX-Refresh would reload this section's
		// own URL, which now shows nothing.
		return redirectAfterCreate(c, qaBase(project)+"/cases")
	}
}

// moveQACasesUI files every selected case (form field case_id, repeated) in
// one section. Ids of another project's cases are skipped by the repo, so a
// tampered form moves only this project's cases.
func moveQACasesUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		sectionID, err := qaOwned(c, s, project, repo.QAKindSection, c.FormValue("section_id"))
		if err != nil {
			return err
		}
		raw := c.Context().PostArgs().PeekMulti("case_id")
		if len(raw) == 0 {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one case.")
		}
		ids := make([]uuid.UUID, 0, len(raw))
		for _, r := range raw {
			id, err := uuid.Parse(string(r))
			if err != nil {
				return fiber.ErrNotFound
			}
			ids = append(ids, id)
		}
		n, err := repo.MoveQACases(c.Context(), s, ids, sectionID)
		if err != nil {
			if status, msg, ok := qaSectionErr(err, ""); ok {
				return qaFormError(c, d, status, msg)
			}
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.cases.moved", strconv.Itoa(n))
		return redirectAfterMutation(c, casesURL(qaBase(project), sectionID.String(), "", false, 1))
	}
}
