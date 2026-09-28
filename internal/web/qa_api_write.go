package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
)

// Writes through the QA API: cases in bulk (the AI-assisted workflow that
// wrote most of the legacy tool's cases), results, and issues.

// apiRowErrors is a 422 naming every refused row.
type apiRowErrors struct{ rows []string }

func (e *apiRowErrors) Error() string { return strings.Join(e.rows, " ") }

// apiCaseIn is one case of a bulk create. Sections along section_path are
// matched by name (ignoring case) and created when missing.
type apiCaseIn struct {
	SectionPath  []string     `json:"section_path"`
	Title        string       `json:"title"`
	Precondition string       `json:"precondition"`
	Steps        repo.QASteps `json:"steps"`
	Priority     *string      `json:"priority"`
	Negative     bool         `json:"negative"`
	Notes        string       `json:"notes"`
}

type qaCreateCasesArgs struct {
	Cases []apiCaseIn `json:"cases"`
}

// checkAPICase is what is wrong with row n (1-based), "" when nothing is.
func checkAPICase(n int, in *apiCaseIn) string {
	fail := func(msg string, a ...any) string { return fmt.Sprintf("Row %d: ", n) + fmt.Sprintf(msg, a...) }
	var path []string
	for _, p := range in.SectionPath {
		if p = strings.TrimSpace(p); p != "" {
			path = append(path, p)
		}
	}
	in.SectionPath = path
	in.Title = strings.TrimSpace(in.Title)
	texts := append([]string{in.Title, in.Precondition, in.Notes}, path...)
	for _, s := range in.Steps {
		texts = append(texts, s.Action, s.Expected)
	}
	for _, s := range texts {
		if strings.ContainsRune(s, 0) {
			return fail("text cannot contain a NUL character.")
		}
	}
	switch {
	case len(path) == 0:
		return fail("section_path is required.")
	case len(path) > repo.MaxQASectionDepth:
		return fail("sections can nest at most %d levels.", repo.MaxQASectionDepth)
	case in.Title == "":
		return fail("title is required.")
	case utf8.RuneCountInString(in.Title) > repo.MaxQATitleLength:
		return fail("title can be at most %d characters.", repo.MaxQATitleLength)
	}
	for _, p := range path {
		if utf8.RuneCountInString(p) > repo.MaxQANameLength {
			return fail("section names can be at most %d characters.", repo.MaxQANameLength)
		}
	}
	if in.Priority != nil {
		p := strings.ToLower(strings.TrimSpace(*in.Priority))
		switch p {
		case "":
			in.Priority = nil
		case "high", "medium", "low":
			in.Priority = &p
		default:
			return fail("priority must be high, medium, low or empty.")
		}
	}
	return ""
}

func qaOpCreateCases(c *fiber.Ctx, call qaCall, a qaCreateCasesArgs) (any, error) {
	if len(a.Cases) == 0 || len(a.Cases) > qaAPIMaxLimit {
		return nil, apiErr(fiber.StatusBadRequest, "send 1 to %d cases per request", qaAPIMaxLimit)
	}
	var rowErrs []string
	check := make([]qaCSVRow, len(a.Cases))
	for i := range a.Cases {
		if msg := checkAPICase(i+1, &a.Cases[i]); msg != "" {
			rowErrs = append(rowErrs, msg)
		}
		check[i] = qaCSVRow{Line: i + 1, SectionPath: a.Cases[i].SectionPath}
	}
	if len(rowErrs) == 0 {
		sections, err := repo.ListQASections(c.Context(), call.s, call.project.ID)
		if err != nil {
			return nil, err
		}
		_, rowErrs = qaImportCheck(check, sections)
	}
	if len(rowErrs) > 0 {
		return nil, &apiRowErrors{rows: rowErrs}
	}
	rows := make([]repo.QACaseImport, len(a.Cases))
	for i, in := range a.Cases {
		rows[i] = repo.QACaseImport{SectionPath: in.SectionPath, Case: repo.QACaseInput{
			Title: in.Title, Precondition: in.Precondition, Steps: in.Steps, Priority: in.Priority, Negative: in.Negative, Notes: in.Notes,
		}}
	}
	keys, sections, err := repo.ImportQACasesKeys(c.Context(), call.s, call.project.ID, rows, qaActorOf(call.p))
	var archived *repo.QASectionArchivedError
	if errors.As(err, &archived) { // archived since the check above
		return nil, &apiRowErrors{rows: []string{fmt.Sprintf("Section '%s' is archived.", archived.Path)}}
	}
	if err != nil {
		return nil, err
	}
	refs := make([]string, len(keys))
	for i, k := range keys {
		refs[i] = fmt.Sprintf("TC-%d", k)
	}
	return fiber.Map{"created": len(keys), "sections_created": sections, "refs": refs}, nil
}

type qaRecordResultArgs struct {
	ItemID  string `json:"item_id"`
	Status  string `json:"status"`
	Comment string `json:"comment"`
}

func qaOpRecordResult(c *fiber.Ctx, call qaCall, a qaRecordResultArgs) (any, error) {
	itemID, err := qaOwned(c, call.s, call.project, repo.QAKindItem, a.ItemID)
	if err != nil {
		return nil, errAPINotFound
	}
	status := strings.TrimSpace(a.Status)
	if !qaResultChoices[status] {
		return nil, apiErr(fiber.StatusBadRequest, "status must be passed, failed, blocked or skipped")
	}
	if _, err := repo.RecordQAResult(c.Context(), call.s, repo.QAResultInput{
		ItemID: itemID, Status: status, Comment: a.Comment, CreatedBy: qaActorOf(call.p),
	}); err != nil {
		if errors.Is(err, repo.ErrQAClosed) {
			return nil, apiErr(fiber.StatusConflict, "this run is closed")
		}
		return nil, err
	}
	it, err := repo.GetQARunItem(c.Context(), call.s, itemID)
	if err != nil {
		return nil, err
	}
	return fiber.Map{"item": apiRunItem{ID: it.ID, CaseID: it.CaseID, Group: it.GroupName, Title: it.Title,
		Precondition: it.Precondition, Steps: it.Steps, Status: it.Status, AssigneeID: it.AssigneeID}}, nil
}

type qaCreateIssueArgs struct {
	Title            string `json:"title"`
	Severity         string `json:"severity"`
	Description      string `json:"description"`
	StepsToReproduce string `json:"steps_to_reproduce"`
	Environment      string `json:"environment"`
	PlanID           string `json:"plan_id"`
	ItemID           string `json:"item_id"`
}

// qaOpCreateIssue reports an issue. From a run item it is linked to the
// item and takes the item's plan; a guest's issue is a client issue.
func qaOpCreateIssue(c *fiber.Ctx, call qaCall, a qaCreateIssueArgs) (any, error) {
	in := repo.QAIssueInput{Title: a.Title, Severity: strings.TrimSpace(a.Severity), Description: a.Description,
		StepsToReproduce: a.StepsToReproduce, Environment: a.Environment, Source: "internal"}
	if authz.IsProjectScoped(call.p) {
		in.Source = "client"
	}
	switch {
	case strings.TrimSpace(in.Title) == "":
		return nil, apiErr(fiber.StatusUnprocessableEntity, "title is required")
	case utf8.RuneCountInString(strings.TrimSpace(in.Title)) > repo.MaxQATitleLength:
		return nil, apiErr(fiber.StatusUnprocessableEntity, "title can be at most %d characters", repo.MaxQATitleLength)
	case !slices.Contains(repo.QAIssueSeverities, in.Severity):
		return nil, apiErr(fiber.StatusUnprocessableEntity, "severity must be one of %s", strings.Join(repo.QAIssueSeverities, ", "))
	}
	if a.PlanID != "" {
		id, err := qaOwned(c, call.s, call.project, repo.QAKindPlan, a.PlanID)
		if err != nil {
			return nil, apiErr(fiber.StatusNotFound, "no such plan in this project")
		}
		in.PlanID = &id
	}
	var fromItem *uuid.UUID
	if a.ItemID != "" {
		item, _, _, err := qaIssueItem(c, call.s, call.project, a.ItemID)
		if err != nil {
			return nil, apiErr(fiber.StatusNotFound, "no such run item in this project")
		}
		fromItem = &item.ID
	}
	is, err := repo.CreateQAIssue(c.Context(), call.s, call.project.ID, in, qaActorOf(call.p), fromItem)
	if err != nil {
		return nil, err
	}
	return fiber.Map{"id": is.ID, "key": is.Key, "ref": fmt.Sprintf("BUG-%d", is.Key), "title": is.Title,
		"severity": is.Severity, "status": is.Status, "source": is.Source, "plan_id": is.PlanID}, nil
}

// bodyInto decodes the JSON body into v as strictly as MCP decodes tool
// arguments: an unknown field is refused, so a misspelt one never becomes
// hundreds of cases created without it.
func bodyInto(c *fiber.Ctx, v any) error {
	if !strings.HasPrefix(c.Get(fiber.HeaderContentType), fiber.MIMEApplicationJSON) {
		return apiErr(fiber.StatusUnsupportedMediaType, "send the body as application/json")
	}
	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apiErr(fiber.StatusBadRequest, "the body must be JSON with the documented fields: %v", err)
	}
	return nil
}

func registerQAAPIWriteRoutes(app *fiber.App, authDeps authn.Deps) {
	write := func(path, perm string, status int, op func(c *fiber.Ctx, call qaCall) (any, error)) {
		app.Post(path, append(qaAPIChain(authDeps, perm), qaAPIHandler(status, true, op))...)
	}
	write("/api/qa/projects/:id/cases", "qa.case.write", fiber.StatusCreated, func(c *fiber.Ctx, call qaCall) (any, error) {
		var a qaCreateCasesArgs
		if err := bodyInto(c, &a); err != nil {
			return nil, err
		}
		return qaOpCreateCases(c, call, a)
	})
	write("/api/qa/projects/:id/items/:iid/result", "qa.execute", fiber.StatusOK, func(c *fiber.Ctx, call qaCall) (any, error) {
		var a qaRecordResultArgs
		if err := bodyInto(c, &a); err != nil {
			return nil, err
		}
		a.ItemID = c.Params("iid")
		return qaOpRecordResult(c, call, a)
	})
	write("/api/qa/projects/:id/issues", "qa.issue.report", fiber.StatusCreated, func(c *fiber.Ctx, call qaCall) (any, error) {
		var a qaCreateIssueArgs
		if err := bodyInto(c, &a); err != nil {
			return nil, err
		}
		return qaOpCreateIssue(c, call, a)
	})
}
