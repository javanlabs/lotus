package web

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// The QA JSON API (/api/qa/…). It and the MCP endpoint (/mcp/qa, which
// calls the same operations) take Lotus API tokens only — a Bearer token,
// never the browser's session cookie — and answer errors as JSON. A token
// can do what both its scopes and its owner's current role allow
// (authz.Principal.Can); a guest's token reaches only the projects it is
// listed on, and a service token pinned to a project only that project.
// Anything out of reach is 404, as in the app.

// qaAPIMaxLimit bounds one page of cases, and one bulk create.
const qaAPIMaxLimit = 500

// apiError is an error with the status and message the caller sees.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

func apiErr(status int, format string, a ...any) error {
	return &apiError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

var errAPINotFound = apiErr(fiber.StatusNotFound, "not found")

// apiErrStatus is the status and message for any error an operation or the
// middleware before it returns.
func apiErrStatus(err error) (int, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status, ae.Msg
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		if fe.Code == fiber.StatusNotFound {
			return fe.Code, "not found"
		}
		return fe.Code, fe.Message
	}
	return repoErrStatus(err)
}

// qaAPILogf records an error the API did not expect (tests replace it).
var qaAPILogf = log.Printf

// apiJSONErrors answers every error below it as {"error": "…"}.
func apiJSONErrors(c *fiber.Ctx) error {
	err := c.Next()
	if err == nil {
		return nil
	}
	var rows *apiRowErrors
	if errors.As(err, &rows) {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "some cases were refused; nothing was created", "errors": rows.rows})
	}
	status, msg := apiErrStatus(err)
	if status >= fiber.StatusInternalServerError {
		qaAPILogf("qa api: %s %s: %v", c.Method(), c.Path(), err)
	}
	if status == fiber.StatusUnauthorized {
		c.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="lotus"`)
	}
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// requireBearer refuses a request without an API token. A browser session
// is not enough: no cookie authority for scripts, no redirect to sign in.
func requireBearer(c *fiber.Ctx) error {
	if !strings.HasPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ") {
		return apiErr(fiber.StatusUnauthorized, "an API token is required (Authorization: Bearer <token>)")
	}
	return c.Next()
}

// qaAPIChain is what every /api/qa route runs before its handler.
func qaAPIChain(authDeps authn.Deps, perm string) []fiber.Handler {
	return []fiber.Handler{apiJSONErrors, tenant.RequireTenant(), requireBearer,
		authn.RequireTokenOrSession(authDeps), authz.Require(perm), qaAPIWithDB(authDeps.DB)}
}

// qaAPIDBKey holds the control-plane connection for the operations that
// name people (a case's PIC and author): users and memberships live
// outside the tenant scope.
const qaAPIDBKey = "qa.api.db"

// qaAPIDB wraps the connection kept in Locals. Never the *sqlx.DB itself:
// fasthttp closes every io.Closer among a request's user values when the
// request ends, which would close the application's pool.
type qaAPIDB struct{ db *sqlx.DB }

func qaAPIWithDB(db *sqlx.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(qaAPIDBKey, qaAPIDB{db})
		return c.Next()
	}
}

// qaCall is who is asking, in which organization, about which project
// (nil for the project list).
type qaCall struct {
	s       tenant.Scope
	p       authz.Principal
	project *repo.Project
	d       Deps // only DB is set (qaAPIWithDB)
}

// newQACall resolves the caller and, when raw is not empty, the project it
// names — if the caller may reach it.
func newQACall(c *fiber.Ctx, raw string, needProject bool) (qaCall, error) {
	s, err := mustScope(c)
	if err != nil {
		return qaCall{}, err
	}
	p, ok := authz.CurrentPrincipal(c)
	if !ok {
		return qaCall{}, apiErr(fiber.StatusUnauthorized, "no principal")
	}
	db, _ := c.Locals(qaAPIDBKey).(qaAPIDB)
	call := qaCall{s: s, p: p, d: Deps{DB: db.db}}
	if !needProject {
		return call, nil
	}
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return call, errAPINotFound
	}
	if p.PinnedProjectID != uuid.Nil && p.PinnedProjectID != id {
		return call, errAPINotFound
	}
	if p.PinnedProjectID == uuid.Nil && p.PinnedComponentID != uuid.Nil {
		// A component pin is an ingest credential, not QA access to the
		// organization: it reaches no project.
		return call, errAPINotFound
	}
	if authz.IsProjectScoped(p) {
		member, err := repo.IsProjectMember(c.Context(), s, id, p.UserID)
		if err != nil {
			return call, err
		}
		if !member {
			return call, errAPINotFound
		}
	}
	project, err := repo.ProjectByID(c.Context(), s, id)
	if errors.Is(err, repo.ErrNotFound) {
		return call, errAPINotFound
	}
	call.project = project
	return call, err
}

// qaAPIProjectScope is the project list the caller's QA reaches: nil for
// every project.
func qaAPIProjectScope(c *fiber.Ctx, call qaCall) ([]uuid.UUID, error) {
	if call.p.PinnedProjectID != uuid.Nil {
		return []uuid.UUID{call.p.PinnedProjectID}, nil
	}
	if call.p.PinnedComponentID != uuid.Nil {
		return []uuid.UUID{}, nil // see newQACall
	}
	if authz.IsProjectScoped(call.p) {
		return repo.ListProjectIDsForMember(c.Context(), call.s, call.p.UserID)
	}
	return nil, nil
}

// qaAPIHandler runs op for a route and answers its result as JSON with
// status. needProject reads the project from :id.
func qaAPIHandler(status int, needProject bool, op func(c *fiber.Ctx, call qaCall) (any, error)) fiber.Handler {
	return func(c *fiber.Ctx) error {
		call, err := newQACall(c, c.Params("id"), needProject)
		if err != nil {
			return err
		}
		out, err := op(c, call)
		if err != nil {
			return err
		}
		return c.Status(status).JSON(out)
	}
}

// qaActorOf is the user an API write is recorded against: nil for an
// organization service token, which has no user.
func qaActorOf(p authz.Principal) *uuid.UUID {
	if p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// ---- Shapes ----

type apiProject struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// apiUser is a person a row points at: a PIC or an author.
type apiUser struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
}

type apiSection struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parent_id"`
	Name     string     `json:"name"`
	Path     []string   `json:"path"`
	Archived bool       `json:"archived"`
	PIC      *apiUser   `json:"pic"`
}

type apiCase struct {
	ID           uuid.UUID    `json:"id"`
	Key          int          `json:"key"`
	Ref          string       `json:"ref"`
	Title        string       `json:"title"`
	SectionID    uuid.UUID    `json:"section_id"`
	SectionPath  []string     `json:"section_path"`
	Precondition string       `json:"precondition"`
	Steps        repo.QASteps `json:"steps"`
	Priority     *string      `json:"priority"`
	Negative     bool         `json:"negative"`
	Notes        string       `json:"notes"`
	Version      int          `json:"version"`
	Archived     bool         `json:"archived"`
	PIC          *apiUser     `json:"pic"`           // the case's own
	EffectivePIC *apiUser     `json:"effective_pic"` // its own, else its nearest section's
	CreatedBy    *apiUser     `json:"created_by"`
}

type apiProgress struct {
	Total    int `json:"total"`
	Untested int `json:"untested"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Blocked  int `json:"blocked"`
	Skipped  int `json:"skipped"`
}

func toAPIProgress(p repo.QAProgress) apiProgress {
	return apiProgress{Total: p.Total, Untested: p.Untested, Passed: p.Passed, Failed: p.Failed, Blocked: p.Blocked, Skipped: p.Skipped}
}

type apiScenario struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Phase       string    `json:"phase"`
	Description string    `json:"description"`
	Archived    bool      `json:"archived"`
}

type apiPlan struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Phase       string      `json:"phase"`
	Environment string      `json:"environment"`
	Build       string      `json:"build"`
	StartsOn    *string     `json:"starts_on"`
	EndsOn      *string     `json:"ends_on"`
	Closed      bool        `json:"closed"`
	Progress    apiProgress `json:"progress"`
}

type apiRun struct {
	ID       uuid.UUID   `json:"id"`
	PlanID   uuid.UUID   `json:"plan_id"`
	Name     string      `json:"name"`
	Cycle    int         `json:"cycle"`
	RetestOf *uuid.UUID  `json:"retest_of"`
	Closed   bool        `json:"closed"`
	Progress apiProgress `json:"progress"`
}

type apiRunItem struct {
	ID           uuid.UUID    `json:"id"`
	CaseID       uuid.UUID    `json:"case_id"`
	CaseRef      string       `json:"case_ref"`
	Group        string       `json:"group"`
	Title        string       `json:"title"`
	Precondition string       `json:"precondition"`
	Steps        repo.QASteps `json:"steps"`
	Status       string       `json:"status"`
	AssigneeID   *uuid.UUID   `json:"assignee_id"`
}

// qaAPIUsers looks up every user in ids; apiUserOf then turns an id into
// the API's shape (nil for none, or a user since deleted).
func qaAPIUsers(c *fiber.Ctx, call qaCall, ids []uuid.UUID) (map[uuid.UUID]global.User, error) {
	return global.UsersByIDs(c.Context(), call.d.DB, ids)
}

func apiUserOf(users map[uuid.UUID]global.User, id *uuid.UUID) *apiUser {
	if id == nil {
		return nil
	}
	u, ok := users[*id]
	if !ok {
		return nil
	}
	out := &apiUser{ID: u.ID, Name: u.Name}
	if u.Email != nil {
		out.Email = *u.Email
	}
	return out
}

func apiDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// qaSectionPaths is each section's path of names, outermost first.
func qaSectionPaths(sections []repo.QASection) map[uuid.UUID][]string {
	byID := make(map[uuid.UUID]repo.QASection, len(sections))
	for _, s := range sections {
		byID[s.ID] = s
	}
	out := make(map[uuid.UUID][]string, len(sections))
	var path func(id uuid.UUID, depth int) []string
	path = func(id uuid.UUID, depth int) []string {
		if p, ok := out[id]; ok {
			return p
		}
		s, ok := byID[id]
		if !ok || depth > repo.MaxQASectionDepth {
			return nil
		}
		var p []string
		if s.ParentID != nil {
			p = append(p, path(*s.ParentID, depth+1)...)
		}
		p = append(p, s.Name)
		out[id] = p
		return p
	}
	for _, s := range sections {
		path(s.ID, 0)
	}
	return out
}

// ---- Read operations ----

func qaOpListProjects(c *fiber.Ctx, call qaCall) (any, error) {
	scope, err := qaAPIProjectScope(c, call)
	if err != nil {
		return nil, err
	}
	projects, err := repo.ListProjects(c.Context(), call.s, false)
	if err != nil {
		return nil, err
	}
	out := []apiProject{}
	for _, p := range projects {
		if scope == nil || slices.Contains(scope, p.ID) {
			out = append(out, apiProject{ID: p.ID, Slug: p.Slug, Name: p.Name})
		}
	}
	return fiber.Map{"projects": out}, nil
}

func qaOpListSections(c *fiber.Ctx, call qaCall) (any, error) {
	sections, err := repo.ListQASections(c.Context(), call.s, call.project.ID)
	if err != nil {
		return nil, err
	}
	paths := qaSectionPaths(sections)
	users, err := qaAPIUsers(c, call, qaPICIDs(sections))
	if err != nil {
		return nil, err
	}
	picOf := map[uuid.UUID]*uuid.UUID{}
	for _, sec := range sections {
		picOf[sec.ID] = sec.PicID
	}
	out := []apiSection{}
	for _, r := range qaSectionTree(sections, nil, true, nil) {
		out = append(out, apiSection{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Path: paths[r.ID], Archived: r.Archived,
			PIC: apiUserOf(users, picOf[r.ID])})
	}
	return fiber.Map{"sections": out}, nil
}

// qaCasesArgs filters the case list: a section (and its subsections), a
// title search, archived cases, and a page.
type qaCasesArgs struct {
	SectionID       string `json:"section_id"`
	Query           string `json:"query"`
	IncludeArchived bool   `json:"include_archived"`
	Limit           int    `json:"limit"`
	Offset          int    `json:"offset"`
}

func qaOpListCases(c *fiber.Ctx, call qaCall, a qaCasesArgs) (any, error) {
	if a.Limit == 0 {
		a.Limit = 100
	}
	if a.Limit < 0 || a.Limit > qaAPIMaxLimit || a.Offset < 0 {
		return nil, apiErr(fiber.StatusBadRequest, "limit must be 1 to %d and offset 0 or more", qaAPIMaxLimit)
	}
	f := repo.QACaseFilter{Query: a.Query, IncludeArchived: a.IncludeArchived, Limit: a.Limit, Offset: a.Offset, IncludeSubsections: true}
	if a.SectionID != "" {
		id, err := qaOwned(c, call.s, call.project, repo.QAKindSection, a.SectionID)
		if err != nil {
			return nil, apiErr(fiber.StatusNotFound, "no such section in this project")
		}
		f.SectionID = &id
	}
	total, err := repo.CountQACases(c.Context(), call.s, call.project.ID, f)
	if err != nil {
		return nil, err
	}
	cases, err := repo.ListQACases(c.Context(), call.s, call.project.ID, f)
	if err != nil {
		return nil, err
	}
	sections, err := repo.ListQASections(c.Context(), call.s, call.project.ID)
	if err != nil {
		return nil, err
	}
	paths := qaSectionPaths(sections)
	var people []*uuid.UUID
	for _, k := range cases {
		people = append(people, k.PicID, k.CreatedBy)
	}
	users, err := qaAPIUsers(c, call, qaPICIDs(sections, people...))
	if err != nil {
		return nil, err
	}
	out := make([]apiCase, len(cases))
	for i, k := range cases {
		effective, _ := repo.QAEffectivePIC(sections, k.SectionID, k.PicID)
		out[i] = apiCase{ID: k.ID, Key: k.Key, Ref: fmt.Sprintf("TC-%d", k.Key), Title: k.Title, SectionID: k.SectionID,
			SectionPath: paths[k.SectionID], Precondition: k.Precondition, Steps: k.Steps, Priority: k.Priority,
			Negative: k.Negative, Notes: k.Notes, Version: k.Version, Archived: k.ArchivedAt != nil,
			PIC: apiUserOf(users, k.PicID), EffectivePIC: apiUserOf(users, effective), CreatedBy: apiUserOf(users, k.CreatedBy)}
	}
	return fiber.Map{"total": total, "limit": a.Limit, "offset": a.Offset, "cases": out}, nil
}

func qaOpListScenarios(c *fiber.Ctx, call qaCall) (any, error) {
	scenarios, err := repo.ListQAScenarios(c.Context(), call.s, call.project.ID, true)
	if err != nil {
		return nil, err
	}
	out := []apiScenario{}
	for _, sc := range scenarios {
		out = append(out, apiScenario{ID: sc.ID, Name: sc.Name, Phase: sc.Phase, Description: sc.Description, Archived: sc.ArchivedAt != nil})
	}
	return fiber.Map{"scenarios": out}, nil
}

func qaOpListPlans(c *fiber.Ctx, call qaCall) (any, error) {
	plans, err := repo.ListQAPlans(c.Context(), call.s, call.project.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(plans))
	for i, p := range plans {
		ids[i] = p.ID
	}
	progress, err := repo.QAPlanProgress(c.Context(), call.s, ids)
	if err != nil {
		return nil, err
	}
	out := []apiPlan{}
	for _, p := range plans {
		out = append(out, apiPlan{ID: p.ID, Name: p.Name, Phase: p.Phase, Environment: p.Environment, Build: p.Build,
			StartsOn: apiDate(p.StartsOn), EndsOn: apiDate(p.EndsOn), Closed: p.ClosedAt != nil, Progress: toAPIProgress(progress[p.ID])})
	}
	return fiber.Map{"plans": out}, nil
}

func qaOpListRuns(c *fiber.Ctx, call qaCall, rawPlan string) (any, error) {
	planID, err := qaOwned(c, call.s, call.project, repo.QAKindPlan, rawPlan)
	if err != nil {
		return nil, errAPINotFound
	}
	runs, err := repo.ListQARuns(c.Context(), call.s, planID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	progress, err := repo.QARunProgressFor(c.Context(), call.s, ids)
	if err != nil {
		return nil, err
	}
	out := []apiRun{}
	for _, r := range runs {
		out = append(out, apiRun{ID: r.ID, PlanID: r.PlanID, Name: r.Name, Cycle: r.Cycle, RetestOf: r.RetestOf,
			Closed: r.ClosedAt != nil, Progress: toAPIProgress(progress[r.ID])})
	}
	return fiber.Map{"runs": out}, nil
}

var qaItemStatuses = []string{"untested", "passed", "failed", "blocked", "skipped"}

// qaRunArgs picks a run and a page of its items, optionally only those in
// one status.
type qaRunArgs struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// qaOpGetRun is a run, its progress, and a page of its items (100 by
// default, at most 500): a real run holds over a thousand, more than an
// assistant can read in one answer.
func qaOpGetRun(c *fiber.Ctx, call qaCall, a qaRunArgs) (any, error) {
	runID, err := qaOwned(c, call.s, call.project, repo.QAKindRun, a.RunID)
	if err != nil {
		return nil, errAPINotFound
	}
	status := a.Status
	if status != "" && !slices.Contains(qaItemStatuses, status) {
		return nil, apiErr(fiber.StatusBadRequest, "status must be one of %s", strings.Join(qaItemStatuses, ", "))
	}
	if a.Limit == 0 {
		a.Limit = 100
	}
	if a.Limit < 0 || a.Limit > qaAPIMaxLimit || a.Offset < 0 {
		return nil, apiErr(fiber.StatusBadRequest, "limit must be 1 to %d and offset 0 or more", qaAPIMaxLimit)
	}
	run, err := repo.GetQARun(c.Context(), call.s, runID)
	if err != nil {
		return nil, err
	}
	all, err := repo.ListQARunItems(c.Context(), call.s, runID, repo.QAItemFilter{})
	if err != nil {
		return nil, err
	}
	var progress repo.QAProgress
	var matching []repo.QARunItem
	for _, it := range all {
		progress.Count(it.Status)
		if status == "" || it.Status == status {
			matching = append(matching, it)
		}
	}
	page := matching[min(a.Offset, len(matching)):min(a.Offset+a.Limit, len(matching))]
	caseIDs := make([]uuid.UUID, len(page))
	for i, it := range page {
		caseIDs[i] = it.CaseID
	}
	keys, err := repo.QACaseKeys(c.Context(), call.s, caseIDs)
	if err != nil {
		return nil, err
	}
	items := []apiRunItem{}
	for _, it := range page {
		items = append(items, apiRunItem{ID: it.ID, CaseID: it.CaseID, CaseRef: fmt.Sprintf("TC-%d", keys[it.CaseID]), Group: it.GroupName,
			Title: it.Title, Precondition: it.Precondition, Steps: it.Steps, Status: it.Status, AssigneeID: it.AssigneeID})
	}
	return fiber.Map{
		"id": run.ID, "plan_id": run.PlanID, "name": run.Name, "cycle": run.Cycle, "retest_of": run.RetestOf,
		"closed": run.ClosedAt != nil, "progress": toAPIProgress(progress),
		"total": len(matching), "limit": a.Limit, "offset": a.Offset, "items": items,
	}, nil
}

// queryInt reads an integer query parameter; missing is 0.
func queryInt(c *fiber.Ctx, name string) (int, error) {
	raw := c.Query(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apiErr(fiber.StatusBadRequest, "%s must be a number", name)
	}
	return n, nil
}

func registerQAAPIRoutes(app *fiber.App, authDeps authn.Deps) {
	read := func(path string, needProject bool, op func(c *fiber.Ctx, call qaCall) (any, error)) {
		app.Get(path, append(qaAPIChain(authDeps, "qa.read"), qaAPIHandler(fiber.StatusOK, needProject, op))...)
	}
	read("/api/qa/projects", false, qaOpListProjects)
	read("/api/qa/projects/:id/sections", true, qaOpListSections)
	read("/api/qa/projects/:id/cases", true, func(c *fiber.Ctx, call qaCall) (any, error) {
		limit, err := queryInt(c, "limit")
		if err != nil {
			return nil, err
		}
		offset, err := queryInt(c, "offset")
		if err != nil {
			return nil, err
		}
		return qaOpListCases(c, call, qaCasesArgs{SectionID: c.Query("section_id"), Query: c.Query("q"),
			IncludeArchived: c.QueryBool("include_archived"), Limit: limit, Offset: offset})
	})
	read("/api/qa/projects/:id/scenarios", true, qaOpListScenarios)
	read("/api/qa/projects/:id/plans", true, qaOpListPlans)
	read("/api/qa/projects/:id/plans/:pid/runs", true, func(c *fiber.Ctx, call qaCall) (any, error) {
		return qaOpListRuns(c, call, c.Params("pid"))
	})
	read("/api/qa/projects/:id/runs/:rid", true, func(c *fiber.Ctx, call qaCall) (any, error) {
		limit, err := queryInt(c, "limit")
		if err != nil {
			return nil, err
		}
		offset, err := queryInt(c, "offset")
		if err != nil {
			return nil, err
		}
		return qaOpGetRun(c, call, qaRunArgs{RunID: c.Params("rid"), Status: c.Query("status"), Limit: limit, Offset: offset})
	})
	registerQAAPIWriteRoutes(app, authDeps)
}
