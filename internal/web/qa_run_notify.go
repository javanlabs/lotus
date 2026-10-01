package web

import (
	"fmt"
	"html"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// A run's new assignee is told by email: when a run is created with one,
// and when editing a run gives it to someone else. Only the new assignee
// hears of it, never someone assigning themselves. The mail goes after the
// change is committed, and a failure to send is logged, never the
// request's failure: the assignment stands, and the run is in their work
// list either way.

// qaSendMail delivers one QA notification (tests replace it). Mail that is
// not set up is not an error.
var qaSendMail = func(m *mailer.Mailer, msg mailer.Message) error {
	if !m.Enabled() {
		return nil
	}
	return m.Send(msg)
}

// notifyQARunAssignee emails run's assignee, unless there is none or it is
// the person acting.
func notifyQARunAssignee(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, plan *repo.QAPlan, run *repo.QARun) {
	if run.AssigneeID == nil {
		return
	}
	if actor := qaActor(c); actor != nil && *actor == *run.AssigneeID {
		return
	}
	if err := sendQARunAssigned(c, d, s, project, plan, run); err != nil {
		log.Printf("qa: run %s assigned mail: %v", run.ID, err)
	}
}

func sendQARunAssigned(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, plan *repo.QAPlan, run *repo.QARun) error {
	org, ok := tenant.OrgFromContext(c)
	if !ok {
		return fmt.Errorf("no organization")
	}
	user, err := global.UserByID(c.Context(), d.DB, *run.AssigneeID)
	if err != nil {
		return err
	}
	if user.Email == nil {
		return nil
	}
	_, per, err := repo.QARunProgress(c.Context(), s, run.ID)
	if err != nil {
		return err
	}
	n := per[*run.AssigneeID].Total
	link := d.Cfg.TenantURL(org.Subdomain) + runURL(project, run)
	return qaSendMail(d.Mailer, mailer.Message{
		To:      mailer.Address{Name: user.Name, Email: *user.Email},
		Subject: fmt.Sprintf("You're assigned the run %s (%s)", run.Name, plan.Name),
		HTML:    qaRunAssignedHTML(run.Name, plan.Name, project.Name, n, link),
	})
}

func qaRunAssignedHTML(run, plan, project string, items int, link string) string {
	count := fmt.Sprintf("%d items", items)
	if items == 1 {
		count = "1 item"
	}
	return "<p>You are now the assignee of the test run <strong>" + html.EscapeString(run) + "</strong>" +
		" in the plan " + html.EscapeString(plan) + " of " + html.EscapeString(project) + ".</p>" +
		"<p>It has " + count + " assigned to you.</p>" +
		"<p><a href=\"" + html.EscapeString(link) + "\">Open the run</a></p>"
}

// qaRunAssignee reads an assignee_id field: "" is nobody, anyone else must
// be someone who can execute here (qaPeople), or the request is 404.
func qaRunAssignee(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project) (*uuid.UUID, error) {
	raw := c.FormValue("assignee_id")
	if raw == "" {
		return nil, nil
	}
	people, err := qaPeople(c, d, s, project)
	if err != nil {
		return nil, err
	}
	for _, p := range people {
		if p.ID.String() == raw {
			id := p.ID
			return &id, nil
		}
	}
	return nil, fiber.ErrNotFound
}
