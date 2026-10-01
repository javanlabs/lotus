package web

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// Managing a run (qa.plan.write): edit it, assign or remove items, start a
// retest, close it, delete it.

// assignQAItemsUI gives every ticked item (item_id, repeated) to one
// person, or to nobody. Ids of another run's items are skipped by the
// repo; the person must be someone who can execute here.
func assignQAItemsUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		ids, err := formUUIDs(c, "item_id")
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one item.")
		}
		var assignee *uuid.UUID
		if raw := strings.TrimSpace(c.FormValue("assignee_id")); raw != "" {
			people, err := qaPeople(c, d, s, project)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, p := range people {
				if p.ID.String() == raw {
					id := p.ID
					assignee = &id
				}
			}
			if assignee == nil {
				return fiber.ErrNotFound
			}
		}
		n, err := repo.AssignQARunItems(c.Context(), s, run.ID, ids, assignee)
		if err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.items.assigned", strconv.Itoa(n))
		return redirectAfterMutation(c, runURL(project, run))
	}
}

// retestQARunUI starts the next cycle with the run's failed and blocked
// items, each given to whoever ran it, and opens it.
func retestQARunUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		retest, err := repo.CreateQARetest(c.Context(), s, run.ID, qaActor(c))
		switch {
		case errors.Is(err, repo.ErrQAEmptyRun):
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Nothing failed or blocked to retest.")
		case errors.Is(err, repo.ErrQAClosed):
			return qaFormError(c, d, fiber.StatusConflict, "This plan is closed.")
		case err != nil:
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.run.created", retest.Name)
		return redirectAfterCreate(c, qaBase(project)+"/runs/"+retest.ID.String())
	}
}

func closeQARunUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		if run.ClosedAt != nil { // already closed: nothing to do, nothing to audit
			return redirectAfterMutation(c, runURL(project, run))
		}
		if err := repo.CloseQARun(c.Context(), s, run.ID); err != nil {
			return writeRepoErr(c, err)
		}
		raw, _ := json.Marshal(fiber.Map{"name": run.Name, "plan_id": run.PlanID, "project_id": project.ID})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.run.close", "qa_run", &run.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.run.closed", run.Name)
		return redirectAfterMutation(c, runURL(project, run))
	}
}

// saveQARunAsScenarioUI keeps a run's case set for next time: a new
// scenario with the run's cases, order and groups (qa.case.write).
func saveQARunAsScenarioUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		name, phase, _, msg := readQAScenarioForm(c)
		if msg != "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, msg)
		}
		sc, err := repo.CreateQAScenarioFromRun(c.Context(), s, run.ID, name, phase, qaActor(c))
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

// updateQARunUI saves an open run's name, notes and assignee. A new
// assignee takes over the items that followed the previous one (the repo
// moves them in the same transaction) and is told by email.
func updateQARunUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		if run.ClosedAt != nil {
			return qaFormError(c, d, fiber.StatusConflict, "This run is closed.")
		}
		assignee, err := qaRunAssignee(c, d, s, project)
		if err != nil {
			return err
		}
		saved, changed, err := repo.UpdateQARun(c.Context(), s, run.ID, repo.QARunUpdate{
			Name: c.FormValue("name"), Notes: c.FormValue("notes"), Assignee: assignee,
		})
		if errors.Is(err, repo.ErrQAClosed) {
			return qaFormError(c, d, fiber.StatusConflict, "This run is closed.")
		}
		if err != nil {
			if status, msg, ok := qaRunErrMessage(err, repo.QARunInput{}); ok {
				return qaFormError(c, d, status, msg)
			}
			return writeRepoErr(c, err)
		}
		if changed {
			plan, err := repo.GetQAPlan(c.Context(), s, saved.PlanID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			notifyQARunAssignee(c, d, s, project, plan, saved)
		}
		setFlash(c, "qa.run.saved", saved.Name)
		return redirectAfterMutation(c, runURL(project, run))
	}
}

// removeQARunItemsUI takes the ticked items (item_id, repeated) out of an
// open run, with their results and files. Issues reported from them stay;
// only the links to the removed items go.
func removeQARunItemsUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		ids, err := formUUIDs(c, "item_id")
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Select at least one item.")
		}
		n, err := repo.RemoveQARunItems(c.Context(), s, d.Blob, run.ID, ids)
		if errors.Is(err, repo.ErrQAClosed) {
			return qaFormError(c, d, fiber.StatusConflict, "This run is closed.")
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		if n > 0 {
			raw, _ := json.Marshal(fiber.Map{"name": run.Name, "removed": n, "plan_id": run.PlanID, "project_id": project.ID})
			if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.run.items.remove", "qa_run", &run.ID, raw); err != nil {
				return writeRepoErr(c, err)
			}
		}
		setFlash(c, "qa.items.removed", strconv.Itoa(n))
		return redirectAfterMutation(c, runURL(project, run))
	}
}

// deleteQARunUI deletes a run, open or closed, and opens its plan. Its
// items, results and files go with it; issues reported from it stay, and
// a retest of it stays, no longer marked as one.
func deleteQARunUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		plan, err := repo.GetQAPlan(c.Context(), s, run.PlanID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := repo.DeleteQARun(c.Context(), s, d.Blob, run.ID); err != nil {
			return writeRepoErr(c, err)
		}
		raw, _ := json.Marshal(fiber.Map{"name": run.Name, "cycle": run.Cycle, "plan_id": run.PlanID, "project_id": project.ID})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.run.delete", "qa_run", &run.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.run.deleted", run.Name)
		return redirectAfterCreate(c, planURL(project, plan))
	}
}
