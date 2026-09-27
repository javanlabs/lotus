package web

import (
	"errors"
	"fmt"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Reporting an issue (qa.issue.report): standalone, or from a run item,
// which pre-fills the title, steps, environment and plan from what the run
// holds and links the issue to the item. A guest's issue is a client issue;
// only people with qa.issue.write choose an assignee.

type qaIssueFormData struct {
	Project          repo.Project
	Base             string
	ItemID           string
	ItemLabel        string // "TC-3 Login · Cycle 1", when reported from an item
	Title            string
	Severity         string
	Description      string
	StepsToReproduce string
	Environment      string
	PlanID           string
	SectionID        string
	AssigneeID       string
	CanAssign        bool
	Error            string
	MaxFileBytes     int
	SeverityOptions  []httpx.SelectOption
	PlanOptions      []httpx.SelectOption
	SectionOptions   []httpx.SelectOption
	PeopleOptions    []httpx.SelectOption
}

// qaStepsText writes a run item's steps as the numbered text an issue
// carries: "1. Open the portal → Login page".
func qaStepsText(steps repo.QASteps) string {
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s", i+1, s.Action)
		if s.Expected != "" {
			fmt.Fprintf(&b, " → %s", s.Expected)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// qaIssueItem reads an item id (query or form) of the project in the URL,
// with its run and plan.
func qaIssueItem(c *fiber.Ctx, s tenant.Scope, project *repo.Project, raw string) (*repo.QARunItem, *repo.QARun, *repo.QAPlan, error) {
	id, err := qaOwned(c, s, project, repo.QAKindItem, raw)
	if err != nil {
		return nil, nil, nil, err
	}
	item, err := repo.GetQARunItem(c.Context(), s, id)
	if err != nil {
		return nil, nil, nil, err
	}
	run, err := repo.GetQARun(c.Context(), s, item.RunID)
	if err != nil {
		return nil, nil, nil, err
	}
	plan, err := repo.GetQAPlan(c.Context(), s, run.PlanID)
	return item, run, plan, err
}

// fillQAIssueOptions fills the pickers of the report form and the edit
// dialog: severities, the project's plans and live sections, and (with
// qa.issue.write) the people an issue can be assigned to.
func fillQAIssueOptions(c *fiber.Ctx, d Deps, s tenant.Scope, f *qaIssueFormData) error {
	project := &f.Project
	f.CanAssign = mustPrincipal(c).Can("qa.issue.write")
	f.MaxFileBytes = repo.MaxQAAttachmentBytes
	f.SeverityOptions = append([]httpx.SelectOption{{Value: "", Label: "Choose…"}}, qaSeverityOptions("")...)
	plans, err := repo.ListQAPlans(c.Context(), s, project.ID)
	if err != nil {
		return err
	}
	f.PlanOptions = []httpx.SelectOption{{Value: "", Label: "No plan"}}
	for _, p := range plans {
		f.PlanOptions = append(f.PlanOptions, httpx.SelectOption{Value: p.ID.String(), Label: p.Name})
	}
	sections, err := repo.ListQASections(c.Context(), s, project.ID)
	if err != nil {
		return err
	}
	f.SectionOptions = append([]httpx.SelectOption{{Value: "", Label: "No section"}},
		qaSectionOptions(qaSectionTree(sections, nil, false, nil), nil)...)
	if f.CanAssign {
		people, err := qaPeople(c, d, s, project)
		if err != nil {
			return err
		}
		f.PeopleOptions = []httpx.SelectOption{{Value: "", Label: "Nobody"}}
		for _, p := range people {
			f.PeopleOptions = append(f.PeopleOptions, httpx.SelectOption{Value: p.ID.String(), Label: p.Label})
		}
	}
	return nil
}

func renderQAIssueForm(c *fiber.Ctx, d Deps, s tenant.Scope, f qaIssueFormData, status int) error {
	project := &f.Project
	if err := fillQAIssueOptions(c, d, s, &f); err != nil {
		return writeRepoErr(c, err)
	}
	org, user, err := shellChrome(c, d.DB)
	if err != nil {
		return err
	}
	nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Issues", Href: f.Base + "/issues"}, httpx.NavCrumb{Label: "Report issue"})
	if err != nil {
		return writeRepoErr(c, err)
	}
	c.Type("html")
	c.Status(status)
	return renderPage(c, d, "pages/qa_issue_new.html", httpx.ViewData{
		Title: "Report issue · " + project.Name, Org: org, User: user, Nav: nav, Data: f,
	})
}

func newQAIssuePage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		f := qaIssueFormData{Project: *project, Base: qaBase(project)}
		if raw := c.Query("item"); raw != "" {
			item, run, plan, err := qaIssueItem(c, s, project, raw)
			if err != nil {
				return err
			}
			refs, err := repo.QACaseRefs(c.Context(), s, []uuid.UUID{item.CaseID})
			if err != nil {
				return writeRepoErr(c, err)
			}
			f.ItemID = item.ID.String()
			f.ItemLabel = fmt.Sprintf("TC-%d %s · %s", refs[item.CaseID].Key, item.Title, run.Name)
			f.Title = truncateRunes("Fail: "+item.Title, repo.MaxQATitleLength)
			f.StepsToReproduce = qaStepsText(item.Steps)
			f.Environment = plan.Environment
			f.PlanID = plan.ID.String()
		}
		return renderQAIssueForm(c, d, s, f, fiber.StatusOK)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// optionalOwned reads an optional id field that must belong to the project.
func optionalOwned(c *fiber.Ctx, s tenant.Scope, project *repo.Project, kind repo.QAKind, raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := qaOwned(c, s, project, kind, raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func createQAIssueUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		f := qaIssueFormData{Project: *project, Base: qaBase(project),
			ItemID: c.FormValue("item_id"), Title: c.FormValue("title"), Severity: strings.TrimSpace(c.FormValue("severity")),
			Description: c.FormValue("description"), StepsToReproduce: c.FormValue("steps_to_reproduce"),
			Environment: c.FormValue("environment"), PlanID: c.FormValue("plan_id"), SectionID: c.FormValue("section_id"),
			AssigneeID: c.FormValue("assignee_id")}
		in := repo.QAIssueInput{Source: "internal"}
		if authz.IsProjectScoped(mustPrincipal(c)) {
			in.Source = "client"
		}
		if err := readQAIssueFields(c, d, s, project, &in); err != nil {
			return err
		}
		var fromItem *uuid.UUID
		if f.ItemID != "" {
			item, _, _, err := qaIssueItem(c, s, project, f.ItemID)
			if err != nil {
				return err
			}
			fromItem = &item.ID
		}
		var files []*multipart.FileHeader
		if form, err := c.MultipartForm(); err == nil {
			files = form.File["file"]
		}
		refuse := func(status int, msg string) error {
			f.Error = msg
			return renderQAIssueForm(c, d, s, f, status)
		}
		switch {
		case in.Severity == "":
			return refuse(fiber.StatusUnprocessableEntity, "Choose a severity.")
		case len(files) > 0 && d.Blob == nil:
			return refuse(fiber.StatusServiceUnavailable, "File storage is not available. Report the issue without files, or ask an administrator.")
		}
		for _, fh := range files {
			if fh.Size > repo.MaxQAAttachmentBytes {
				return refuse(fiber.StatusUnprocessableEntity, "Each file can be at most 20 MB.")
			}
		}
		is, err := repo.CreateQAIssue(c.Context(), s, project.ID, in, qaActor(c), fromItem)
		if errors.Is(err, repo.ErrInvalidInput) {
			return refuse(fiber.StatusUnprocessableEntity,
				fmt.Sprintf("Title is required and can be at most %d characters.", repo.MaxQATitleLength))
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := saveQAFiles(c, d, s, repo.QAAttachmentOwner{IssueID: &is.ID}, files); err != nil {
			return err
		}
		setFlash(c, "qa.issue.reported", "BUG-"+strconv.Itoa(is.Key))
		return c.Redirect(qaBase(project)+"/issues/"+strconv.Itoa(is.Key), fiber.StatusFound)
	}
}

// saveQAFiles stores each file for owner. A browser that does not know a
// file's type says octet-stream; SaveQAAttachment sniffs it instead.
func saveQAFiles(c *fiber.Ctx, d Deps, s tenant.Scope, owner repo.QAAttachmentOwner, files []*multipart.FileHeader) error {
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return err
		}
		ctype := fh.Header.Get("Content-Type")
		if ctype == "application/octet-stream" {
			ctype = ""
		}
		_, err = repo.SaveQAAttachment(c.Context(), s, d.Blob, owner, fh.Filename, ctype, f, qaActor(c))
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
