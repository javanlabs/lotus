package web

import (
	"errors"
	"fmt"
	"mime/multipart"
	"slices"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The issue page (qa.read) shows an issue's fields, its files, the run
// items it was found in and its timeline. Editing fields and changing the
// status take qa.issue.write; commenting, with files, takes
// qa.issue.report, so a guest can add to its issues but not triage them.

// loadQAIssue reads :key within the project in the URL.
func loadQAIssue(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (*repo.QAIssue, error) {
	key, err := strconv.Atoi(c.Params("key"))
	if err != nil || key < 1 {
		return nil, fiber.ErrNotFound
	}
	is, err := repo.GetQAIssueByKey(c.Context(), s, project.ID, key)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, fiber.ErrNotFound
	}
	return is, err
}

func issueURL(project *repo.Project, is *repo.QAIssue) string {
	return fmt.Sprintf("%s/issues/%d", qaBase(project), is.Key)
}

type qaIssueEventView struct {
	repo.QAIssueEvent
	By    string
	Files []qaFileLink
}

type qaIssueItemView struct {
	Key     int
	Title   string
	Status  string
	Tone    string
	RunName string
	RunHref string
}

type qaIssueData struct {
	Project       repo.Project
	Base          string
	Issue         repo.QAIssue
	URL           string
	SeverityTone  string
	ReportedBy    string
	Assignee      string
	PlanName      string
	PlanHref      string
	SectionPath   string
	Files         []qaFileLink
	Items         []qaIssueItemView
	Events        []qaIssueEventView
	CanWrite      bool // qa.issue.write: edit, status
	CanComment    bool // qa.issue.report
	MaxFileBytes  int
	Form          qaIssueFormData
	StatusOptions []httpx.SelectOption
}

func qaFileLinks(c *fiber.Ctx, s tenant.Scope, project *repo.Project, owner repo.QAAttachmentOwner) ([]qaFileLink, error) {
	files, err := repo.ListQAAttachments(c.Context(), s, owner)
	if err != nil {
		return nil, err
	}
	out := make([]qaFileLink, 0, len(files))
	for _, f := range files {
		out = append(out, qaFileLink{Name: f.FileName, Href: qaBase(project) + "/files/" + f.ID.String(), Size: humanSize(f.SizeBytes)})
	}
	return out, nil
}

func qaIssuePage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		is, err := loadQAIssue(c, s, project)
		if err != nil {
			return err
		}
		labels, err := qaPeopleLabels(c, d, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		p := mustPrincipal(c)
		data := qaIssueData{Project: *project, Base: qaBase(project), Issue: *is, URL: issueURL(project, is),
			SeverityTone: qaSeverityTone(is.Severity), ReportedBy: personLabel(labels, is.ReportedBy),
			Assignee: personLabel(labels, is.AssigneeID), CanWrite: p.Can("qa.issue.write"),
			CanComment: p.Can("qa.issue.report"), MaxFileBytes: repo.MaxQAAttachmentBytes}
		if is.PlanID != nil {
			if plan, err := repo.GetQAPlan(c.Context(), s, *is.PlanID); err == nil {
				data.PlanName, data.PlanHref = plan.Name, planURL(project, plan)
			}
		}
		if is.SectionID != nil {
			sections, err := repo.ListQASections(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			for _, r := range qaSectionTree(sections, nil, true, nil) {
				if r.ID == *is.SectionID {
					data.SectionPath = r.Path
				}
			}
		}
		if data.Files, err = qaFileLinks(c, s, project, repo.QAAttachmentOwner{IssueID: &is.ID}); err != nil {
			return writeRepoErr(c, err)
		}
		items, err := repo.ListQAItemsForIssue(c.Context(), s, is.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		caseIDs := make([]uuid.UUID, len(items))
		for i, it := range items {
			caseIDs[i] = it.CaseID
		}
		refs, err := repo.QACaseRefs(c.Context(), s, caseIDs)
		if err != nil {
			return writeRepoErr(c, err)
		}
		runs := map[uuid.UUID]*repo.QARun{}
		for _, it := range items {
			run, ok := runs[it.RunID]
			if !ok {
				if run, err = repo.GetQARun(c.Context(), s, it.RunID); err != nil {
					return writeRepoErr(c, err)
				}
				runs[it.RunID] = run
			}
			data.Items = append(data.Items, qaIssueItemView{Key: refs[it.CaseID].Key, Title: it.Title, Status: it.Status,
				Tone: qaStatusTone(it.Status), RunName: run.Name, RunHref: runURL(project, run)})
		}
		events, err := repo.ListQAIssueEvents(c.Context(), s, is.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		for _, e := range events {
			id := e.ID
			v := qaIssueEventView{QAIssueEvent: e, By: personLabel(labels, e.ActorID)}
			if v.Files, err = qaFileLinks(c, s, project, repo.QAAttachmentOwner{EventID: &id}); err != nil {
				return writeRepoErr(c, err)
			}
			data.Events = append(data.Events, v)
		}
		if data.CanWrite {
			for _, st := range repo.QAIssueStatuses {
				data.StatusOptions = append(data.StatusOptions, httpx.SelectOption{Value: st, Label: st})
			}
			data.Form = qaIssueFormData{Project: *project, Base: data.Base, Title: is.Title, Severity: is.Severity,
				Description: is.Description, StepsToReproduce: is.StepsToReproduce, Environment: is.Environment,
				PlanID: uuidString(is.PlanID), SectionID: uuidString(is.SectionID), AssigneeID: uuidString(is.AssigneeID)}
			if err := fillQAIssueOptions(c, d, s, &data.Form); err != nil {
				return writeRepoErr(c, err)
			}
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Issues", Href: data.Base + "/issues"},
			httpx.NavCrumb{Label: "BUG-" + strconv.Itoa(is.Key)})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_issue.html", httpx.ViewData{
			Title: fmt.Sprintf("BUG-%d %s", is.Key, is.Title), Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

// readQAIssueFields reads the fields both the report form and the edit
// dialog send, checking every id against the project and the assignee
// against the people who can execute here (404 otherwise).
func readQAIssueFields(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, in *repo.QAIssueInput) error {
	in.Title, in.Severity = c.FormValue("title"), strings.TrimSpace(c.FormValue("severity"))
	in.Description, in.StepsToReproduce = c.FormValue("description"), c.FormValue("steps_to_reproduce")
	in.Environment = c.FormValue("environment")
	var err error
	if in.PlanID, err = optionalOwned(c, s, project, repo.QAKindPlan, c.FormValue("plan_id")); err != nil {
		return err
	}
	if in.SectionID, err = optionalOwned(c, s, project, repo.QAKindSection, c.FormValue("section_id")); err != nil {
		return err
	}
	in.AssigneeID = nil
	if raw := strings.TrimSpace(c.FormValue("assignee_id")); raw != "" && mustPrincipal(c).Can("qa.issue.write") {
		people, err := qaPeople(c, d, s, project)
		if err != nil {
			return err
		}
		for _, p := range people {
			if p.ID.String() == raw {
				id := p.ID
				in.AssigneeID = &id
			}
		}
		if in.AssigneeID == nil {
			return fiber.ErrNotFound
		}
	}
	return nil
}

func updateQAIssueUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		is, err := loadQAIssue(c, s, project)
		if err != nil {
			return err
		}
		in := repo.QAIssueInput{Source: is.Source}
		if err := readQAIssueFields(c, d, s, project, &in); err != nil {
			return err
		}
		if in.Severity == "" {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Choose a severity.")
		}
		if err := repo.UpdateQAIssue(c.Context(), s, is.ID, in); err != nil {
			if errors.Is(err, repo.ErrInvalidInput) {
				return qaFormError(c, d, fiber.StatusUnprocessableEntity,
					fmt.Sprintf("Title is required and can be at most %d characters.", repo.MaxQATitleLength))
			}
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.issue.saved", "BUG-"+strconv.Itoa(is.Key))
		return redirectAfterMutation(c, issueURL(project, is))
	}
}

func changeQAIssueStatusUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		is, err := loadQAIssue(c, s, project)
		if err != nil {
			return err
		}
		to := strings.TrimSpace(c.FormValue("status"))
		if !slices.Contains(repo.QAIssueStatuses, to) {
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Choose a status.")
		}
		if err := repo.ChangeQAIssueStatus(c.Context(), s, is.ID, to, qaActor(c), c.FormValue("note")); err != nil {
			return writeRepoErr(c, err)
		}
		return redirectAfterMutation(c, issueURL(project, is))
	}
}

func commentQAIssueUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		is, err := loadQAIssue(c, s, project)
		if err != nil {
			return err
		}
		var files []*multipart.FileHeader
		if form, err := c.MultipartForm(); err == nil {
			files = form.File["file"]
		}
		switch {
		case strings.TrimSpace(c.FormValue("body")) == "":
			return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Write a comment.")
		case len(files) > 0 && d.Blob == nil:
			return qaFormError(c, d, fiber.StatusServiceUnavailable, "File storage is not available. Comment without files, or ask an administrator.")
		}
		for _, fh := range files {
			if fh.Size > repo.MaxQAAttachmentBytes {
				return qaFormError(c, d, fiber.StatusUnprocessableEntity, "Each file can be at most 20 MB.")
			}
		}
		ev, err := repo.CommentQAIssue(c.Context(), s, is.ID, c.FormValue("body"), qaActor(c))
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := saveQAFiles(c, d, s, repo.QAAttachmentOwner{EventID: &ev.ID}, files); err != nil {
			return err
		}
		return redirectAfterMutation(c, issueURL(project, is))
	}
}
