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

// Managing a run (qa.plan.write): assign items, start a retest, close it.

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
