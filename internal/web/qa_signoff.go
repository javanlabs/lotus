package web

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// Sign-off (qa.signoff): the client's verdict on a plan, for the whole plan
// or one of its scenarios. The plan page shows the standing one per scope;
// the audit log keeps every one.

var qaSignoffLabels = map[string]string{
	"accepted":            "Accepted",
	"accepted_with_notes": "Accepted with notes",
	"rejected":            "Rejected",
}

var qaSignoffTones = map[string]string{
	"accepted":            "pass",
	"accepted_with_notes": "none",
	"rejected":            "fail",
}

// qaSignoffRow is one scope of a plan and where it stands.
type qaSignoffRow struct {
	Scope    string // "Whole plan" or the scenario's name
	Decision string // "" when nobody has signed this scope off
	Label    string
	Tone     string
	Note     string
	SignedBy string
	SignedAt string
}

// qaPlanSignoffs lists the whole plan, then each of its scenarios, with the
// latest sign-off of each. namesOnly is for the shared report: a signer is
// shown by name, never by email address (no name shows "—").
func qaPlanSignoffs(c *fiber.Ctx, d Deps, s tenant.Scope, planID uuid.UUID, scenarios []qaScenarioRow, namesOnly bool) ([]qaSignoffRow, error) {
	latest, err := repo.ListLatestQASignoffs(c.Context(), s, planID)
	if err != nil {
		return nil, err
	}
	labels, err := qaPeopleLabels(c, d, s)
	if err != nil {
		return nil, err
	}
	if namesOnly {
		if labels, err = qaPeopleNames(c, d, s); err != nil {
			return nil, err
		}
	}
	byScope := map[uuid.UUID]repo.QASignoff{}
	for _, so := range latest {
		key := uuid.Nil
		if so.ScenarioID != nil {
			key = *so.ScenarioID
		}
		byScope[key] = so
	}
	row := func(scope string, key uuid.UUID) qaSignoffRow {
		r := qaSignoffRow{Scope: scope}
		if so, ok := byScope[key]; ok {
			r.Decision, r.Label, r.Tone, r.Note = so.Decision, qaSignoffLabels[so.Decision], qaSignoffTones[so.Decision], so.Note
			r.SignedBy, r.SignedAt = personLabel(labels, so.SignedBy), so.SignedAt.Format("02 Jan 2006 15:04")
		}
		return r
	}
	out := []qaSignoffRow{row("Whole plan", uuid.Nil)}
	for _, sc := range scenarios {
		out = append(out, row(sc.Name, sc.ID))
	}
	return out, nil
}

// qaPeopleNames is qaPeopleLabels without the email fallback: members with
// no name are left out.
func qaPeopleNames(c *fiber.Ctx, d Deps, s tenant.Scope) (map[uuid.UUID]string, error) {
	members, err := global.ListMembersForOrg(c.Context(), d.DB, s.OrgID())
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(members))
	for _, m := range members {
		if name := strings.TrimSpace(m.Name); name != "" {
			out[m.UserID] = name
		}
	}
	return out, nil
}

// qaSignoffScopes are the choices of the sign-off form.
func qaSignoffScopes(scenarios []qaScenarioRow) []httpx.SelectOption {
	out := []httpx.SelectOption{{Value: "plan", Label: "Whole plan"}}
	for _, sc := range scenarios {
		out = append(out, httpx.SelectOption{Value: sc.ID.String(), Label: sc.Name})
	}
	return out
}

func recordQASignoffUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		in := repo.QASignoffInput{Decision: c.FormValue("decision"), Note: c.FormValue("note")}
		if scope := strings.TrimSpace(c.FormValue("scope")); scope != "plan" {
			id, err := uuid.Parse(scope)
			if err != nil {
				return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Choose the plan or one of its scenarios.")
			}
			in.ScenarioID = &id
		}
		if _, ok := qaSignoffLabels[in.Decision]; !ok {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Choose a decision.")
		}
		if in.Decision != "accepted" && strings.TrimSpace(in.Note) == "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Say what is wrong in the note.")
		}
		so, err := repo.RecordQASignoff(c.Context(), s, plan.ID, in, qaActor(c))
		if errors.Is(err, repo.ErrInvalidInput) {
			if in.ScenarioID != nil {
				// The note and decision were checked above: what is left
				// is a scenario that is not in this plan.
				return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Choose the plan or one of its scenarios.")
			}
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "The note is too long.")
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		raw, _ := json.Marshal(fiber.Map{"plan_id": plan.ID, "scenario_id": so.ScenarioID, "decision": so.Decision})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.signoff", "qa_plan", &plan.ID, raw); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.signoff.recorded", qaSignoffLabels[so.Decision])
		return redirectAfterMutation(c, planURL(project, plan))
	}
}
