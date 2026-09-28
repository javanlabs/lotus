# QA module — Phase 8 (part 1): QA API and MCP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give scripts and AI assistants the QA module: a JSON API under `/api/qa` and an MCP server at `/mcp/qa`. Both use Lotus API tokens, and the MCP server keeps the legacy tool's AI bulk-case workflow, which wrote 73% of its new cases.

**Architecture:** The API's operations are plain functions (`qaOp…` over a `qaCall`: scope, principal, project) behind a thin route adapter. The MCP endpoint is a second adapter over the same functions, so the checks cannot drift apart. Authentication is the existing token path (`authn.RequireTokenOrSession`, `authz.Principal.Can`), with the browser session refused. Project reach is the app's rule plus the service-token pin. Bulk creation reuses the CSV import's transaction (`repo.ImportQACases`), which now also reports the keys it gave.

**Tech Stack:** Go 1.26, Fiber v2, Postgres 16+, MCP (streamable HTTP, JSON responses, protocol 2025-06-18 / 2025-03-26 / 2024-11-05).

**Spec:** `docs/superpowers/specs/2026-09-27-qa-module-design.md` (§5 permissions, §8 rollout). **Parent plan:** `docs/superpowers/plans/2026-09-27-qa-module.md`, "Phase 8", tasks 8.1 and 8.2. Tasks 8.3 (ceo-monitor summary), 8.4 (legacy redirects) and 8.5 (cut-over) follow in a later plan. **Previous phases:** `…/2026-09-27-qa-phase-4.md` to `…-phase-7.md` (helpers used here: `qaOwned`, `qaSectionTree`, `qaImportCheck`, `qaIssueItem`, `qaResultChoices`, `repoErrStatus`, `mustScope`, `mustPrincipal`; test helpers `newQARunEnv`, `qaRunEnv.firstRun`).

**Status:** Validated. Every task was implemented in a scratch worktree exactly as written, one commit per task. Each commit on its own passes `go build`, `go vet`, `gofmt`, `make lint` and the full Go suite. The code is also kept as `docs/superpowers/plans/2026-09-28-qa-phase-8-api-mcp.patch` (applies to `main` at `381d738`) for reference; the plan is the authority.

## Decisions

- **API tokens only.** `/api/qa` and `/mcp/qa` refuse a request without `Authorization: Bearer`, even one carrying a valid session cookie. A script or assistant never borrows a browser's session, a missing token is a clean 401 (not a redirect to sign in), and cookie-based CSRF has nothing to reach.
- **A token is its scopes intersected with its owner's current role.** Settings → Tokens gives a new token its creator's role permissions. A token created before the QA permissions existed (Phase 1) has no `qa.*` scope and must be recreated to use this API.
- **Guests do not get tokens** (Phase 7: no `token.manage.own`). The API still applies a guest's project list to a guest-owned token, so the rule does not depend on that.
- **Writes by a service token are recorded against no user**, since it has none. Case, result and issue rows then show no author.
- **No files, assignees or status changes through the API.** The outline's operations are create cases, record a result and report an issue. Attaching files, assigning and triage stay in the app.
- **MCP without OAuth.** The server is stateless, with no session id, no server-sent events and no `Mcp-Session-Id`. The client sends the Lotus token as a static `Authorization` header, which Claude Code (`--header`), Claude Desktop and the common clients support. MCP's OAuth flow is out of scope.
- **`list_runs` is a ninth tool**, beyond the outline's eight, because `get_run` needs a run id and nothing else lists runs.

## Global Constraints

- Everything in Phases 4–7's Global Constraints still holds.
- Permissions exactly as spec §5, applied per route and per tool:
  - `qa.read`: every read and the MCP endpoint itself.
  - `qa.case.write`: bulk create.
  - `qa.execute`: record a result.
  - `qa.issue.report`: report an issue.
- Bulk create: at most 500 cases per request, all or none; keys consecutive per request.
- Case list pages: 100 by default, at most 500.
- Errors are JSON: `{"error": "…"}`, plus `errors: [...]` for refused rows. MCP tool failures are `isError` results, and protocol errors are JSON-RPC errors.

## Review Focus

1. **Token reach.** A token narrower than its owner's role, a guest-owned token, a service token pinned to one project, a token on another organization's host: each must be held to exactly its reach. Pinned in Tasks 1 and 3.
2. **A browser session on `/api/qa` or `/mcp/qa`** must be 401, never a page and never a redirect. Pinned in Task 1 and the guest crawl.
3. **Bulk create under failure and concurrency.** One bad row, or an archived section, creates nothing. Concurrent batches get disjoint, consecutive keys. Pinned in Task 2.
4. **MCP tool parity with the API.** Each tool's permission is its route's, and a tool the token may not call is absent from `tools/list` and refused by `tools/call`. Pinned in Task 3.
5. **Hostile input.** Unknown tool or method, malformed JSON, unknown argument fields, over-limit pages, and markup in titles or steps (stored and returned as data). Pinned in Tasks 1–3.

## File Structure

| File | Responsibility |
|---|---|
| `internal/web/qa_api.go` | Auth chain, JSON errors, project reach, shapes, read operations, read routes |
| `internal/web/qa_api_write.go` | Bulk create, record result, report issue |
| `internal/web/qa_mcp.go` | MCP JSON-RPC over the operations |
| `internal/repo/qa_case_import.go` | `ImportQACasesKeys` |
| `internal/web/settings_tokens.go`, `pages/settings_tokens.html` | Where the API and MCP are, how to connect |

## Running tests

```bash
export TEST_DATABASE_URL='postgres://postgres@localhost:5418/lotus_test?sslmode=disable'
go test ./internal/web -run <Name> -v
```

---

## Tasks

### Task 1: The QA API: token auth, access rules, reads

`/api/qa/…` answers JSON to Lotus API tokens only. A request without `Authorization: Bearer` is 401 with `WWW-Authenticate`. A browser session cookie is no credential here: no redirect to sign in, and no ambient cookie authority for scripts. Every error, including those from the auth middleware, comes back as `{"error": "…"}` (`apiJSONErrors`). What a token may do is what `authz.Principal.Can` already says: both its scopes and its owner's current role for a personal token, and its scopes for a service token. Project reach follows the app: a guest's token reaches only the projects the guest is listed on, a service token pinned to a project only that one, and anything else is 404. The read operations list projects, sections (with full paths), cases (paged 100 by default, 500 at most, filtered by section subtree, title search and archived), scenarios, plans and runs with progress, and a run with its items (optionally one status). They are plain functions (`qaOp…`) so the MCP endpoint in Task 3 calls the same code.

**Files:**
- Create: `internal/web/qa_api.go`
- Create: `internal/web/qa_api_test.go`
- Modify: `internal/web/qa_guest_shell_test.go`
- Modify: `internal/web/server.go`

**Interfaces:**
- Produces (web, `qa_api.go`):
  - `qaAPIMaxLimit = 500`; `apiError{Status; Msg}`, `apiErr(status, format, …)`, `errAPINotFound`, `apiErrStatus(err)`, `apiJSONErrors`, `requireBearer`.
  - `qaAPIChain(authDeps, perm) []fiber.Handler`.
  - `qaCall{s, p, project}`, `newQACall(c, rawProject, needProject)`, `qaAPIProjectScope`, `qaAPIHandler(status, needProject, op)`, `qaActorOf(p)`.
  - Shapes `apiProject`, `apiSection`, `apiCase`, `apiProgress`, `apiScenario`, `apiPlan`, `apiRun`, `apiRunItem`; `qaSectionPaths(sections)`.
  - Operations `qaOpListProjects`, `qaOpListSections`, `qaOpListCases(c, call, qaCasesArgs)`, `qaOpListScenarios`, `qaOpListPlans`, `qaOpListRuns(c, call, rawPlan)`, `qaOpGetRun(c, call, rawRun, status)`; `qaItemStatuses`; `registerQAAPIRoutes(app, authDeps)`, registered in `server.go` after `registerIngestRoutes`.
- Produces (routes, `qa.read`): `GET /api/qa/projects`, `…/projects/:id/sections`, `…/cases?section_id&q&include_archived&limit&offset`, `…/scenarios`, `…/plans`, `…/plans/:pid/runs`, `…/runs/:rid?status`.
- Produces (tests): `qaAPIEnv` (member, read-only, guest and pinned service tokens; `other` project), `qaAPIEnv.api(t, method, token, path, body)`, `decodeJSON[T]`. The guest crawl now expects 401 for a browser session on `/api/qa/…`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/qa_api_test.go`:

```go
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
)

// qaAPIEnv is newQARunEnv plus API tokens: a member's, one narrowed to
// qa.read, a guest's (listed on Portal only) and an organization service
// token pinned to Portal. other is a project none of the narrow ones reach.
type qaAPIEnv struct {
	qaRunEnv
	memberToken, readToken, guestToken, pinnedToken string
	other                                           uuid.UUID
}

func newQAAPIEnv(t *testing.T, sub string) qaAPIEnv {
	t.Helper()
	r := newQARunEnv(t, sub)
	ctx := context.Background()
	deps := authn.Deps{DB: r.db}
	pat := func(userID uuid.UUID, scopes []string) string {
		raw, err := authn.IssuePersonalAccessToken(ctx, deps, authn.NewPersonalAccessTokenParams{UserID: userID, Name: "api test", Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	e := qaAPIEnv{qaRunEnv: r, other: r.project(t, "Mobile")}
	e.memberToken = pat(r.memberID, authz.PermissionsForRole("member"))
	e.readToken = pat(r.memberID, []string{"qa.read"})
	e.guestToken = pat(r.guestID, authz.PermissionsForRole("guest"))
	raw, err := authn.IssueServiceToken(ctx, deps, authn.NewServiceTokenParams{
		OrgID: r.orgID, ActorID: r.memberID, ActorRole: "admin", Name: "ci",
		Scopes: []string{"qa.read", "qa.case.write", "qa.execute", "qa.issue.report"}, ProjectID: &r.projectID,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.pinnedToken = raw
	return e
}

// api sends a JSON request with a Bearer token ("" sends none) and returns
// the status and body.
func (e qaAPIEnv) api(t *testing.T, method, token, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "http://"+e.host+path, rd)
	req.Host = e.host
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func decodeJSON[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

func TestQAAPI_TokensOnlyAndJSONErrors(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-auth")

	status, body := e.api(t, http.MethodGet, "", "/api/qa/projects", nil)
	if status != http.StatusUnauthorized || decodeJSON[map[string]string](t, body)["error"] == "" {
		t.Fatalf("no token = %d %s; want 401 with a JSON error", status, body)
	}
	// A browser session is not an API credential: no redirect to sign in,
	// no ambient cookie authority.
	req := reqWithSession(http.MethodGet, e.host, "/api/qa/projects", e.member)
	resp, err := e.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("session cookie = %d (WWW-Authenticate %q); want 401", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if status, _ := e.api(t, http.MethodGet, "lotus_pat_notARealTokenAtAll1234", "/api/qa/projects", nil); status != http.StatusUnauthorized {
		t.Errorf("bad token = %d; want 401", status)
	}
	status, body = e.api(t, http.MethodGet, e.memberToken, "/api/qa/projects", nil)
	projects := decodeJSON[struct {
		Projects []struct{ ID, Name string } `json:"projects"`
	}](t, body)
	if status != http.StatusOK || len(projects.Projects) != 2 {
		t.Fatalf("member projects = %d %s", status, body)
	}
	// A token narrower than its owner's role is held to the token.
	noQA, _ := authn.IssuePersonalAccessToken(context.Background(), authn.Deps{DB: e.db},
		authn.NewPersonalAccessTokenParams{UserID: e.memberID, Name: "metrics only", Scopes: []string{"dashboard.read"}})
	if status, body := e.api(t, http.MethodGet, noQA, "/api/qa/projects", nil); status != http.StatusForbidden || !strings.Contains(body, `"error"`) {
		t.Errorf("token without qa.read = %d %s; want 403 JSON", status, body)
	}
}

func TestQAAPI_ReadsTheLibraryPlansAndRuns(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-read")
	base := "/api/qa/projects/" + e.projectID.String()

	status, body := e.api(t, http.MethodGet, e.readToken, base+"/sections", nil)
	sections := decodeJSON[struct {
		Sections []struct {
			ID, Name string
			Path     []string
		} `json:"sections"`
	}](t, body)
	if status != http.StatusOK || len(sections.Sections) != 1 || sections.Sections[0].Path[0] != "Auth" {
		t.Fatalf("sections = %d %s", status, body)
	}

	type apiCaseList struct {
		Total int `json:"total"`
		Cases []struct {
			Key         int      `json:"key"`
			Ref         string   `json:"ref"`
			Title       string   `json:"title"`
			SectionPath []string `json:"section_path"`
		} `json:"cases"`
	}
	status, body = e.api(t, http.MethodGet, e.readToken, base+"/cases", nil)
	cases := decodeJSON[apiCaseList](t, body)
	if status != http.StatusOK || cases.Total != 2 || cases.Cases[0].Ref != "TC-1" || cases.Cases[0].SectionPath[0] != "Auth" {
		t.Fatalf("cases = %d %s", status, body)
	}
	_, body = e.api(t, http.MethodGet, e.readToken, base+"/cases?q=logout", nil)
	if got := decodeJSON[apiCaseList](t, body); got.Total != 1 || got.Cases[0].Title != "Logout" {
		t.Errorf("search = %s", body)
	}
	_, body = e.api(t, http.MethodGet, e.readToken, base+"/cases?limit=1&offset=1", nil)
	if got := decodeJSON[apiCaseList](t, body); got.Total != 2 || len(got.Cases) != 1 || got.Cases[0].Key != 2 {
		t.Errorf("page 2 = %s", body)
	}
	if status, _ := e.api(t, http.MethodGet, e.readToken, base+"/cases?limit=5000", nil); status != http.StatusBadRequest {
		t.Errorf("limit over 500 = %d; want 400", status)
	}

	if _, body = e.api(t, http.MethodGet, e.readToken, base+"/scenarios", nil); !strings.Contains(body, `"Regression core"`) {
		t.Errorf("scenarios = %s", body)
	}
	_, body = e.api(t, http.MethodGet, e.readToken, base+"/plans", nil)
	plans := decodeJSON[struct {
		Plans []struct{ ID, Name string } `json:"plans"`
	}](t, body)
	if len(plans.Plans) != 1 || plans.Plans[0].Name != "Regression October" {
		t.Fatalf("plans = %s", body)
	}
	_, body = e.api(t, http.MethodGet, e.readToken, base+"/plans/"+plans.Plans[0].ID+"/runs", nil)
	runs := decodeJSON[struct {
		Runs []struct {
			ID       string          `json:"id"`
			Progress repo.QAProgress `json:"progress"`
		} `json:"runs"`
	}](t, body)
	if len(runs.Runs) != 1 || runs.Runs[0].Progress.Total != 2 {
		t.Fatalf("runs = %s", body)
	}
	status, body = e.api(t, http.MethodGet, e.readToken, base+"/runs/"+runs.Runs[0].ID, nil)
	run := decodeJSON[struct {
		Name  string `json:"name"`
		Items []struct {
			ID, Status, Title, CaseRef string
		} `json:"items"`
	}](t, body)
	if status != http.StatusOK || run.Name != "Cycle 1" || len(run.Items) != 2 || run.Items[0].Status != "untested" {
		t.Fatalf("run = %d %s", status, body)
	}
	if !strings.Contains(body, `"case_ref":"TC-`) {
		t.Errorf("run items carry no case_ref: %s", body)
	}
	_, body = e.api(t, http.MethodGet, e.readToken, base+"/runs/"+runs.Runs[0].ID+"?status=passed", nil)
	if got := decodeJSON[struct {
		Items []struct{ Status string } `json:"items"`
	}](t, body); len(got.Items) != 0 {
		t.Errorf("status filter ignored: %s", body)
	}
	// A plan or run id of another project is 404 under this one.
	if status, _ := e.api(t, http.MethodGet, e.readToken, "/api/qa/projects/"+e.other.String()+"/runs/"+runs.Runs[0].ID, nil); status != http.StatusNotFound {
		t.Errorf("run through another project = %d; want 404", status)
	}
}

func TestQAAPI_GuestAndPinnedTokensReachOnlyTheirProject(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-scope")
	for name, token := range map[string]string{"guest": e.guestToken, "pinned": e.pinnedToken} {
		status, body := e.api(t, http.MethodGet, token, "/api/qa/projects", nil)
		if status != http.StatusOK || !strings.Contains(body, e.projectID.String()) || strings.Contains(body, e.other.String()) {
			t.Errorf("%s projects = %d %s; want Portal only", name, status, body)
		}
		for _, path := range []string{"/sections", "/cases", "/plans", "/scenarios"} {
			if status, _ := e.api(t, http.MethodGet, token, "/api/qa/projects/"+e.other.String()+path, nil); status != http.StatusNotFound {
				t.Errorf("%s on another project %s = %d; want 404", name, path, status)
			}
		}
		if status, _ := e.api(t, http.MethodGet, token, "/api/qa/projects/"+e.projectID.String()+"/cases", nil); status != http.StatusOK {
			t.Errorf("%s on its project = %d; want 200", name, status)
		}
	}
}
```

Apply to `internal/web/qa_guest_shell_test.go`:

```diff
--- a/internal/web/qa_guest_shell_test.go
+++ b/internal/web/qa_guest_shell_test.go
@@ -42,6 +42,12 @@ func TestGuestShell_CrawlEveryGetRoute(t *testing.T) {
 				}
 			case route.Path == "/projects":
 				// The landing page sends a guest home (TestGuestShell_HomeIsMyWork).
+			case strings.HasPrefix(route.Path, "/api/qa/"):
+				// The QA API takes API tokens only; a browser session is
+				// no credential there (TestQAAPI_TokensOnlyAndJSONErrors).
+				if status != http.StatusUnauthorized {
+					t.Errorf("guest session GET %s = %d, want 401", route.Path, status)
+				}
 			case status != http.StatusNotFound:
 				t.Errorf("guest GET %s (project listed=%v) = %d, want 404", route.Path, project == listed, status)
 			}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/web -run 'TestQAAPI|TestGuestShell' -v`
Expected: FAIL — `GET /api/qa/projects` = 404 `Cannot GET`; the scope test gets 404 for the guest's and the pinned token's own project.

- [ ] **Step 3: Implement**

Create `internal/web/qa_api.go`:

```go
package web

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
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

// apiJSONErrors answers every error below it as {"error": "…"}.
func apiJSONErrors(c *fiber.Ctx) error {
	err := c.Next()
	if err == nil {
		return nil
	}
	status, msg := apiErrStatus(err)
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
		authn.RequireTokenOrSession(authDeps), authz.Require(perm)}
}

// qaCall is who is asking, in which organization, about which project
// (nil for the project list).
type qaCall struct {
	s       tenant.Scope
	p       authz.Principal
	project *repo.Project
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
	call := qaCall{s: s, p: p}
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

type apiSection struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parent_id"`
	Name     string     `json:"name"`
	Path     []string   `json:"path"`
	Archived bool       `json:"archived"`
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
	out := []apiSection{}
	for _, r := range qaSectionTree(sections, nil, true, nil) {
		out = append(out, apiSection{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Path: paths[r.ID], Archived: r.Archived})
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
	out := make([]apiCase, len(cases))
	for i, k := range cases {
		out[i] = apiCase{ID: k.ID, Key: k.Key, Ref: fmt.Sprintf("TC-%d", k.Key), Title: k.Title, SectionID: k.SectionID,
			SectionPath: paths[k.SectionID], Precondition: k.Precondition, Steps: k.Steps, Priority: k.Priority,
			Negative: k.Negative, Notes: k.Notes, Version: k.Version, Archived: k.ArchivedAt != nil}
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

// qaOpGetRun is a run with its items, optionally only those in status.
func qaOpGetRun(c *fiber.Ctx, call qaCall, rawRun, status string) (any, error) {
	runID, err := qaOwned(c, call.s, call.project, repo.QAKindRun, rawRun)
	if err != nil {
		return nil, errAPINotFound
	}
	if status != "" && !slices.Contains(qaItemStatuses, status) {
		return nil, apiErr(fiber.StatusBadRequest, "status must be one of %s", strings.Join(qaItemStatuses, ", "))
	}
	run, err := repo.GetQARun(c.Context(), call.s, runID)
	if err != nil {
		return nil, err
	}
	all, err := repo.ListQARunItems(c.Context(), call.s, runID, repo.QAItemFilter{})
	if err != nil {
		return nil, err
	}
	cases, err := repo.ListQACases(c.Context(), call.s, call.project.ID, repo.QACaseFilter{IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	refs := make(map[uuid.UUID]string, len(cases))
	for _, k := range cases {
		refs[k.ID] = fmt.Sprintf("TC-%d", k.Key)
	}
	var progress repo.QAProgress
	items := []apiRunItem{}
	for _, it := range all {
		progress.Count(it.Status)
		if status != "" && it.Status != status {
			continue
		}
		items = append(items, apiRunItem{ID: it.ID, CaseID: it.CaseID, CaseRef: refs[it.CaseID], Group: it.GroupName,
			Title: it.Title, Precondition: it.Precondition, Steps: it.Steps, Status: it.Status, AssigneeID: it.AssigneeID})
	}
	return fiber.Map{
		"id": run.ID, "plan_id": run.PlanID, "name": run.Name, "cycle": run.Cycle, "retest_of": run.RetestOf,
		"closed": run.ClosedAt != nil, "progress": toAPIProgress(progress), "items": items,
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
		return qaOpGetRun(c, call, c.Params("rid"), c.Query("status"))
	})
}
```

Apply to `internal/web/server.go`:

```diff
--- a/internal/web/server.go
+++ b/internal/web/server.go
@@ -190,6 +190,7 @@ func New(d Deps) *fiber.App {
 	registerScoringDocsRoutes(app, d, authn.RequireTenantSession(authDeps), authz.LoadPrincipal(d.DB))
 
 	registerIngestRoutes(app, d, authDeps)
+	registerQAAPIRoutes(app, authDeps)
 
 	return app
 }
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/web -run 'TestQAAPI|TestGuestShell' -v`
Expected: PASS.

Then: `go test ./... && make lint`
Expected: PASS, `check-repo-scope: ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/web/qa_api.go internal/web/qa_api_test.go internal/web/qa_guest_shell_test.go internal/web/server.go
git commit -m "feat(qa): JSON API for QA reads, API tokens only"
```

---

### Task 2: The QA API: bulk cases, results, issues

Three writes. **Bulk create** takes 1 to 500 cases in one request, all or none. Each names its section by path (created when missing, matched ignoring case). Every row is checked first, and a refused batch answers 422 with every bad row named (`Row n: …`), including rows that run through an archived section. It returns the new refs in row order. Keys are consecutive within a request and never shared across concurrent requests (the per-project key lock from Phase 1). **Record a result** takes passed, failed, blocked or skipped with a comment; a closed run is 409. **Report an issue** requires a title and a severity. From a run item it links the item and takes its plan, and a guest's issue is a client issue. Writes are recorded against the token's user, or no one for a service token.

**Files:**
- Create: `internal/web/qa_api_write.go`
- Create: `internal/web/qa_api_write_test.go`
- Modify: `internal/repo/qa_case_import.go`
- Modify: `internal/web/qa_api.go`

**Interfaces:**
- Produces (repo): `ImportQACasesKeys(ctx, s, projectID, rows, createdBy) (keys []int, sections int, err error)`; `ImportQACases` now wraps it.
- Produces (web, `qa_api_write.go`): `apiRowErrors` (422 with `errors`), `apiCaseIn`, `qaCreateCasesArgs{Cases}`, `checkAPICase(n, *apiCaseIn) string`, `qaOpCreateCases`, `qaRecordResultArgs{ItemID, Status, Comment}`, `qaOpRecordResult`, `qaCreateIssueArgs{Title, Severity, Description, StepsToReproduce, Environment, PlanID, ItemID}`, `qaOpCreateIssue`, `bodyInto(c, v)`, `registerQAAPIWriteRoutes` (called at the end of `registerQAAPIRoutes`).
- Produces (routes): `POST /api/qa/projects/:id/cases` (`qa.case.write`, 201), `POST …/items/:iid/result` (`qa.execute`), `POST …/issues` (`qa.issue.report`, 201).
- Produces (tests): `apiCaseRows(n, section...)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/qa_api_write_test.go`:

```go
package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

type apiCreatedCases struct {
	Created         int      `json:"created"`
	SectionsCreated int      `json:"sections_created"`
	Refs            []string `json:"refs"`
	Errors          []string `json:"errors"`
	Error           string   `json:"error"`
}

func apiCaseRows(n int, section ...string) []map[string]any {
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = map[string]any{
			"section_path": section, "title": fmt.Sprintf("Generated case %d", i+1),
			"steps": []map[string]string{{"action": "Open <b>the</b> page", "expected": "It loads"}},
		}
	}
	return rows
}

func TestQAAPI_BulkCreateCasesIsAtomic(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-bulk")
	path := "/api/qa/projects/" + e.projectID.String() + "/cases"
	rows := apiCaseRows(3, "Billing", "Invoices")
	rows[1]["priority"], rows[1]["negative"], rows[1]["notes"] = "high", true, "From the spec"

	status, body := e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": rows})
	got := decodeJSON[apiCreatedCases](t, body)
	// The library holds TC-1 and TC-2 already; keys continue from there.
	if status != http.StatusCreated || got.Created != 3 || got.SectionsCreated != 2 || strings.Join(got.Refs, ",") != "TC-3,TC-4,TC-5" {
		t.Fatalf("bulk create = %d %s", status, body)
	}
	k, err := repo.GetQACaseByKey(context.Background(), e.s, e.projectID, 4)
	if err != nil || k.Priority == nil || *k.Priority != "high" || !k.Negative || k.Notes != "From the spec" || k.Steps[0].Action != "Open <b>the</b> page" {
		t.Fatalf("TC-4 = %+v, %v", k, err)
	}

	// One bad row refuses the whole batch, and every bad row is named.
	bad := apiCaseRows(3, "Billing")
	bad[0]["title"] = "  "
	bad[2]["priority"] = "urgent"
	status, body = e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": bad})
	got = decodeJSON[apiCreatedCases](t, body)
	if status != http.StatusUnprocessableEntity || len(got.Errors) != 2 || !strings.HasPrefix(got.Errors[0], "Row 1:") || !strings.HasPrefix(got.Errors[1], "Row 3:") {
		t.Fatalf("bad rows = %d %s", status, body)
	}
	// An archived section on the way refuses it too.
	sections, _ := repo.ListQASections(context.Background(), e.s, e.projectID)
	for _, s := range sections {
		if s.Name == "Invoices" {
			if err := repo.ArchiveQASection(context.Background(), e.s, s.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, body = e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": apiCaseRows(2, "Billing", "Invoices")})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "archived") {
		t.Fatalf("archived section = %d %s", status, body)
	}
	if n, _ := repo.CountQACases(context.Background(), e.s, e.projectID, repo.QACaseFilter{IncludeArchived: true}); n != 5 {
		t.Fatalf("cases after refused batches = %d; want 5", n)
	}

	for name, body := range map[string]any{
		"empty":    map[string]any{"cases": []any{}},
		"over 500": map[string]any{"cases": apiCaseRows(501, "Bulk")},
	} {
		if status, _ := e.api(t, http.MethodPost, e.memberToken, path, body); status != http.StatusBadRequest {
			t.Errorf("%s = %d; want 400", name, status)
		}
	}
	// Writing needs qa.case.write in the token, not only in the role.
	if status, _ := e.api(t, http.MethodPost, e.readToken, path, map[string]any{"cases": apiCaseRows(1, "Auth")}); status != http.StatusForbidden {
		t.Errorf("read-only token = %d; want 403", status)
	}
	if status, _ := e.api(t, http.MethodPost, e.guestToken, path, map[string]any{"cases": apiCaseRows(1, "Auth")}); status != http.StatusForbidden {
		t.Errorf("guest token = %d; want 403", status)
	}
}

func TestQAAPI_ConcurrentBulkCreatesGetSequentialKeys(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-concurrent")
	path := "/api/qa/projects/" + e.projectID.String() + "/cases"
	var wg sync.WaitGroup
	results := make([][]string, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, body := e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": apiCaseRows(25, fmt.Sprintf("Batch %d", i))})
			if status != http.StatusCreated {
				t.Errorf("batch %d = %d %s", i, status, body)
				return
			}
			results[i] = decodeJSON[apiCreatedCases](t, body).Refs
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	var keys []int
	for i, refs := range results {
		// Each batch's keys are one unbroken run.
		for j := 1; j < len(refs); j++ {
			var a, b int
			fmt.Sscanf(refs[j-1], "TC-%d", &a)
			fmt.Sscanf(refs[j], "TC-%d", &b)
			if b != a+1 {
				t.Errorf("batch %d keys not consecutive: %v", i, refs)
				break
			}
		}
		for _, r := range refs {
			if seen[r] {
				t.Errorf("key %s given twice", r)
			}
			seen[r] = true
			var k int
			fmt.Sscanf(r, "TC-%d", &k)
			keys = append(keys, k)
		}
	}
	sort.Ints(keys)
	if len(keys) != 100 || keys[0] != 3 || keys[99] != 102 {
		t.Fatalf("keys = %d, from %v to %v; want TC-3 to TC-102", len(keys), keys[0], keys[len(keys)-1])
	}
}

func TestQAAPI_RecordResultAndReportIssue(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-execute")
	base := "/api/qa/projects/" + e.projectID.String()
	run, items := e.firstRun(t)

	status, body := e.api(t, http.MethodPost, e.guestToken, base+"/items/"+items[0].ID.String()+"/result",
		map[string]string{"status": "failed", "comment": "500 on submit"})
	if status != http.StatusOK || !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("guest records a result = %d %s", status, body)
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/items/"+items[1].ID.String()+"/result", map[string]string{"status": "untested"}); status != http.StatusBadRequest {
		t.Errorf("status untested = %d; want 400", status)
	}
	if status, _ := e.api(t, http.MethodPost, e.readToken, base+"/items/"+items[1].ID.String()+"/result", map[string]string{"status": "passed"}); status != http.StatusForbidden {
		t.Errorf("read-only token = %d; want 403", status)
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, "/api/qa/projects/"+e.other.String()+"/items/"+items[1].ID.String()+"/result", map[string]string{"status": "passed"}); status != http.StatusNotFound {
		t.Errorf("item through another project = %d; want 404", status)
	}

	// An issue from the failed item: linked to it, its plan filled in, and
	// a client issue because a guest reported it.
	status, body = e.api(t, http.MethodPost, e.guestToken, base+"/issues", map[string]string{
		"title": "Submit returns 500", "severity": "critical", "item_id": items[0].ID.String(), "description": "Seen twice",
	})
	created := decodeJSON[struct {
		Key    int    `json:"key"`
		Ref    string `json:"ref"`
		Source string `json:"source"`
	}](t, body)
	if status != http.StatusCreated || created.Ref != fmt.Sprintf("BUG-%d", created.Key) || created.Source != "client" {
		t.Fatalf("guest issue = %d %s", status, body)
	}
	is, _ := repo.GetQAIssueByKey(context.Background(), e.s, e.projectID, created.Key)
	if is.PlanID == nil || *is.PlanID != run.PlanID {
		t.Errorf("issue plan = %v; want the run's plan", is.PlanID)
	}
	if linked, _ := repo.ListQAIssuesForItem(context.Background(), e.s, items[0].ID); len(linked) != 1 {
		t.Errorf("item links %d issues; want 1", len(linked))
	}
	for name, in := range map[string]map[string]string{
		"no severity":      {"title": "x"},
		"unknown severity": {"title": "x", "severity": "huge"},
		"no title":         {"severity": "minor"},
	} {
		if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/issues", in); status != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d; want 422", name, status)
		}
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/issues", map[string]string{"title": "x", "severity": "minor", "plan_id": items[0].ID.String()}); status != http.StatusNotFound {
		t.Errorf("plan_id that is not a plan here = %d; want 404", status)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/web -run 'TestQAAPI_(Bulk|Concurrent|Record)' -v`
Expected: FAIL — `POST …/cases` = 405 Method Not Allowed (only the read routes exist).

- [ ] **Step 3: Implement**

Apply to `internal/repo/qa_case_import.go`:

```diff
--- a/internal/repo/qa_case_import.go
+++ b/internal/repo/qa_case_import.go
@@ -31,13 +31,21 @@ func (e *QASectionArchivedError) Error() string { return fmt.Sprintf("section %q
 // Keys continue from the project's highest. It returns how many cases and
 // sections it created.
 func ImportQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, rows []QACaseImport, createdBy *uuid.UUID) (cases, sections int, err error) {
+	keys, sections, err := ImportQACasesKeys(ctx, s, projectID, rows, createdBy)
+	return len(keys), sections, err
+}
+
+// ImportQACasesKeys is ImportQACases returning the created cases' keys, in
+// row order. They are consecutive: key allocation is locked per project
+// for the whole transaction.
+func ImportQACasesKeys(ctx context.Context, s tenant.Scope, projectID uuid.UUID, rows []QACaseImport, createdBy *uuid.UUID) (keys []int, sections int, err error) {
 	inputs := make([]QACaseInput, len(rows))
 	for i, r := range rows {
 		if len(r.SectionPath) == 0 || len(r.SectionPath) > MaxQASectionDepth {
-			return 0, 0, ErrInvalidInput
+			return nil, 0, ErrInvalidInput
 		}
 		if inputs[i], err = r.Case.normalized(); err != nil {
-			return 0, 0, err
+			return nil, 0, err
 		}
 	}
 	err = s.Tx(ctx, func(tx *sqlx.Tx) error {
@@ -98,13 +106,13 @@ func ImportQACases(ctx context.Context, s tenant.Scope, projectID uuid.UUID, row
 				s.OrgID(), projectID, *parent, key, in.Title, in.Precondition, in.Notes, in.Steps, in.Priority, in.Negative, createdBy); err != nil {
 				return err
 			}
+			keys = append(keys, key)
 			key++
-			cases++
 		}
 		return nil
 	})
 	if err != nil {
-		return 0, 0, qaWriteErr(err)
+		return nil, 0, qaWriteErr(err)
 	}
-	return cases, sections, nil
+	return keys, sections, nil
 }
```

Apply to `internal/web/qa_api.go`:

```diff
--- a/internal/web/qa_api.go
+++ b/internal/web/qa_api.go
@@ -65,6 +65,10 @@ func apiJSONErrors(c *fiber.Ctx) error {
 	if err == nil {
 		return nil
 	}
+	var rows *apiRowErrors
+	if errors.As(err, &rows) {
+		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "some cases were refused; nothing was created", "errors": rows.rows})
+	}
 	status, msg := apiErrStatus(err)
 	if status == fiber.StatusUnauthorized {
 		c.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="lotus"`)
@@ -517,4 +521,5 @@ func registerQAAPIRoutes(app *fiber.App, authDeps authn.Deps) {
 	read("/api/qa/projects/:id/runs/:rid", true, func(c *fiber.Ctx, call qaCall) (any, error) {
 		return qaOpGetRun(c, call, c.Params("rid"), c.Query("status"))
 	})
+	registerQAAPIWriteRoutes(app, authDeps)
 }
```

Create `internal/web/qa_api_write.go`:

```go
package web

import (
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

// bodyInto decodes the JSON body into v.
func bodyInto(c *fiber.Ctx, v any) error {
	if err := c.BodyParser(v); err != nil {
		return apiErr(fiber.StatusBadRequest, "the body must be JSON: %v", err)
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
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/web -run 'TestQAAPI_(Bulk|Concurrent|Record)' -v`
Expected: PASS.

Then: `go test ./... && make lint`
Expected: PASS, `check-repo-scope: ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/web/qa_api_write.go internal/web/qa_api_write_test.go internal/repo/qa_case_import.go internal/web/qa_api.go
git commit -m "feat(qa): JSON API writes — bulk cases, results, issues"
```

---

### Task 3: The MCP endpoint and where to find it

`POST /mcp/qa` is an MCP server over the streamable HTTP transport, stateless, one JSON-RPC message per request and one JSON answer. It does `initialize` (echoes 2025-06-18, 2025-03-26 or 2024-11-05, otherwise offers the newest), `ping`, `tools/list` and `tools/call`; notifications get 202 with no body, and an unknown method is −32601. GET is 405: there is no server stream. It sits behind the same chain as the API, so an unauthenticated call is 401 (the legacy `X-Public` hole cannot reappear). Each tool declares the permission of its API route and runs the same `qaOp…`. `tools/list` shows only the tools the token may call. A failure a tool meets (permission, project out of reach, bad row, bad arguments) is a result with `isError: true` and a message the assistant can act on. Arguments are decoded strictly, so a mistyped field is reported, not ignored. Tools: `list_projects`, `list_sections`, `list_cases`, `create_cases`, `list_plans`, `list_runs`, `get_run`, `record_result`, `create_issue`. `list_runs` is added to the outline's list because `get_run` needs a run id. Settings → Tokens gains a card with the API and MCP URLs and a client config snippet.

**Files:**
- Create: `internal/web/qa_mcp.go`
- Create: `internal/web/qa_mcp_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/settings_tokens.go`
- Modify: `web/templates/pages/settings_tokens.html`

**Interfaces:**
- Produces (web, `qa_mcp.go`): `mcpProtocolVersions`, `mcpInstructions`, `mcpTool{Name, Perm, Description, Schema, Run}`, `mcpTools`, `mcpArgs(raw, v)` (strict), `mcpProjectTool[A](op)`, `mcpToolError(err)`, `qaMCP()`, `registerQAMCPRoutes(app, authDeps)` (registered in `server.go` after `registerQAAPIRoutes`).
- Changes (web): the tokens page data gains `QAOrigin` (`d.Cfg.TenantURL` of the organization).
- Produces (tests): `qaAPIEnv.mcp(t, token, method, params)`, `qaAPIEnv.callTool(t, token, name, args)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/qa_mcp_test.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

// mcp sends one JSON-RPC request to /mcp/qa.
func (e qaAPIEnv) mcp(t *testing.T, token, method string, params any) (int, mcpResponse) {
	t.Helper()
	status, body := e.api(t, http.MethodPost, token, "/mcp/qa", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	var out mcpResponse
	if body != "" {
		_ = json.Unmarshal([]byte(body), &out)
	}
	return status, out
}

func (e qaAPIEnv) callTool(t *testing.T, token, name string, args map[string]any) mcpToolResult {
	t.Helper()
	status, resp := e.mcp(t, token, "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK || resp.Error != nil {
		t.Fatalf("tools/call %s = %d %+v", name, status, resp.Error)
	}
	return decodeJSON[mcpToolResult](t, string(resp.Result))
}

func TestQAMCP_HandshakeAndToolList(t *testing.T) {
	e := newQAAPIEnv(t, "acme-mcp")

	// The legacy endpoint answered anyone who sent X-Public; this one
	// answers no one without a token.
	if status, _ := e.api(t, http.MethodPost, "", "/mcp/qa", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); status != http.StatusUnauthorized {
		t.Fatalf("no token = %d; want 401", status)
	}
	status, resp := e.mcp(t, e.memberToken, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	init := decodeJSON[struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct{ Name string }
	}](t, string(resp.Result))
	if status != http.StatusOK || init.ProtocolVersion != "2025-06-18" || init.Capabilities["tools"] == nil || init.ServerInfo.Name == "" {
		t.Fatalf("initialize = %d %s", status, resp.Result)
	}
	if status, body := e.api(t, http.MethodPost, e.memberToken, "/mcp/qa", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); status != http.StatusAccepted || body != "" {
		t.Errorf("notification = %d %q; want 202, no body", status, body)
	}

	names := func(token string) string {
		_, resp := e.mcp(t, token, "tools/list", nil)
		list := decodeJSON[struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		}](t, string(resp.Result))
		var out []string
		for _, tool := range list.Tools {
			if tool.Description == "" || tool.InputSchema["type"] != "object" {
				t.Errorf("tool %s has no description or object schema", tool.Name)
			}
			out = append(out, tool.Name)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := names(e.memberToken); got != "create_cases,create_issue,get_run,list_cases,list_plans,list_projects,list_runs,list_sections,record_result" {
		t.Errorf("member tools = %s", got)
	}
	// A token only sees the tools it may call.
	if got := names(e.readToken); got != "get_run,list_cases,list_plans,list_projects,list_runs,list_sections" {
		t.Errorf("read-only tools = %s", got)
	}

	if _, resp := e.mcp(t, e.memberToken, "resources/list", nil); resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method = %+v; want -32601", resp.Error)
	}
	if _, resp := e.mcp(t, e.memberToken, "tools/call", map[string]any{"name": "drop_database"}); resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("unknown tool = %+v; want -32602", resp.Error)
	}
	if status, _ := e.api(t, http.MethodGet, e.memberToken, "/mcp/qa", nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d; want 405 (no server stream)", status)
	}
}

func TestQAMCP_ToolsCallTheAPIWithItsChecks(t *testing.T) {
	e := newQAAPIEnv(t, "acme-mcp-tools")
	project := e.projectID.String()

	res := e.callTool(t, e.memberToken, "list_projects", nil)
	if res.IsError || len(res.StructuredContent["projects"].([]any)) != 2 || res.Content[0].Type != "text" || !strings.Contains(res.Content[0].Text, "Portal") {
		t.Fatalf("list_projects = %+v", res)
	}
	res = e.callTool(t, e.memberToken, "create_cases", map[string]any{"project_id": project, "cases": apiCaseRows(2, "AI", "Generated")})
	if res.IsError || res.StructuredContent["created"].(float64) != 2 {
		t.Fatalf("create_cases = %+v", res)
	}
	res = e.callTool(t, e.memberToken, "list_cases", map[string]any{"project_id": project, "query": "generated"})
	if res.IsError || res.StructuredContent["total"].(float64) != 2 {
		t.Fatalf("list_cases = %+v", res)
	}
	plans := e.callTool(t, e.memberToken, "list_plans", map[string]any{"project_id": project}).StructuredContent["plans"].([]any)
	plan := plans[0].(map[string]any)["id"].(string)
	runs := e.callTool(t, e.memberToken, "list_runs", map[string]any{"project_id": project, "plan_id": plan}).StructuredContent["runs"].([]any)
	run := runs[0].(map[string]any)["id"].(string)
	items := e.callTool(t, e.memberToken, "get_run", map[string]any{"project_id": project, "run_id": run}).StructuredContent["items"].([]any)
	item := items[0].(map[string]any)["id"].(string)
	if res = e.callTool(t, e.memberToken, "record_result", map[string]any{"project_id": project, "item_id": item, "status": "failed"}); res.IsError {
		t.Fatalf("record_result = %+v", res)
	}
	if res = e.callTool(t, e.memberToken, "create_issue", map[string]any{"project_id": project, "item_id": item, "title": "Broken", "severity": "major"}); res.IsError || res.StructuredContent["ref"] == nil {
		t.Fatalf("create_issue = %+v", res)
	}

	// The same checks as the API: the token's scopes, the project it may
	// reach, and the operation's own validation — each an error result the
	// assistant can read.
	for name, tc := range map[string]struct {
		token, tool string
		args        map[string]any
		want        string
	}{
		"scope":      {e.readToken, "create_cases", map[string]any{"project_id": project, "cases": apiCaseRows(1, "AI")}, "permission"},
		"guest":      {e.guestToken, "list_cases", map[string]any{"project_id": e.other.String()}, "not found"},
		"pinned":     {e.pinnedToken, "list_plans", map[string]any{"project_id": e.other.String()}, "not found"},
		"no project": {e.memberToken, "list_sections", map[string]any{}, "not found"},
		"bad row":    {e.memberToken, "create_cases", map[string]any{"project_id": project, "cases": []map[string]any{{"title": "x"}}}, "Row 1: section_path is required."},
		"bad args":   {e.memberToken, "list_cases", map[string]any{"project_id": project, "limit": "many"}, "arguments"},
	} {
		res := e.callTool(t, tc.token, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Errorf("%s: %+v; want an error mentioning %q", name, res, tc.want)
		}
	}
}

// Settings → Tokens says where the QA API and the MCP endpoint are and
// how an assistant sends the token.
func TestQAMCP_TokensPageShowsTheEndpoints(t *testing.T) {
	e := newQAAPIEnv(t, "acme-mcp-settings")
	status, body := e.get(t, e.member, "/settings/tokens")
	if status != http.StatusOK || !strings.Contains(body, "/mcp/qa") || !strings.Contains(body, "/api/qa/projects") || !strings.Contains(body, "Authorization") {
		t.Fatalf("tokens page = %d; no QA API / MCP instructions", status)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/web -run 'TestQAMCP' -v`
Expected: FAIL — `no token = 404; want 401` (no route yet); the tokens page has no QA API / MCP card.

- [ ] **Step 3: Implement**

Create `internal/web/qa_mcp.go`:

```go
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authn"
)

// POST /mcp/qa is an MCP server (streamable HTTP transport) for the QA
// module, so an AI assistant can read the library and runs, write cases in
// bulk, record results and report issues. It is stateless: each POST
// carries one JSON-RPC message and gets one JSON answer (no event stream,
// so GET is 405). Authentication is the QA API's — a Lotus API token,
// never anonymous — and every tool runs the same operation, with the same
// permission and project checks, as its /api/qa route.

// mcpProtocolVersions are the MCP revisions this server speaks, newest
// first; initialize answers with the client's if it is one of them.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = "Lotus QA: test cases (refs TC-n) filed in nested sections, scenarios, test plans, " +
	"runs of a plan with one item per case, and issues (refs BUG-n). Start with list_projects; every other tool " +
	"takes a project_id from it. create_cases writes up to 500 cases at once, all or none, creating sections " +
	"along each section_path. record_result and create_issue take item ids from get_run."

// mcpTool is one tool: its permission (the same as its API route), its
// description and input schema, and how it runs.
type mcpTool struct {
	Name        string
	Perm        string
	Description string
	Schema      map[string]any
	Run         func(c *fiber.Ctx, args json.RawMessage) (any, error)
}

func mcpObject(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	mcpString  = map[string]any{"type": "string"}
	mcpProject = map[string]any{"type": "string", "description": "Project id, from list_projects."}
	mcpSteps   = map[string]any{"type": "array", "items": mcpObject([]string{"action"}, map[string]any{
		"action": mcpString, "expected": mcpString,
	})}
)

// mcpArgs decodes a tool's arguments strictly: an unknown field is an
// error the assistant sees, not a silently ignored typo.
func mcpArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(orEmpty(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
	}
	return nil
}

type mcpProjectArg struct {
	ProjectID string `json:"project_id"`
}

// mcpProjectTool is a tool whose arguments are A plus project_id.
func mcpProjectTool[A any](op func(c *fiber.Ctx, call qaCall, a A) (any, error)) func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
	return func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
		// project_id and the tool's own fields arrive side by side.
		var p mcpProjectArg
		if err := json.Unmarshal(orEmpty(raw), &p); err != nil {
			return nil, apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(orEmpty(raw), &fields); err != nil {
			return nil, apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
		}
		delete(fields, "project_id")
		rest, _ := json.Marshal(fields)
		var args A
		if err := mcpArgs(rest, &args); err != nil {
			return nil, err
		}
		call, err := newQACall(c, p.ProjectID, true)
		if err != nil {
			return nil, err
		}
		return op(c, call, args)
	}
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

type mcpNoArgs struct{}

type mcpPlanArgs struct {
	PlanID string `json:"plan_id"`
}

type mcpRunArgs struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

var mcpTools = []mcpTool{
	{
		Name: "list_projects", Perm: "qa.read",
		Description: "List the projects this token can reach, with their ids.",
		Schema:      mcpObject(nil, map[string]any{}),
		Run: func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
			var a mcpNoArgs
			if err := mcpArgs(raw, &a); err != nil {
				return nil, err
			}
			call, err := newQACall(c, "", false)
			if err != nil {
				return nil, err
			}
			return qaOpListProjects(c, call)
		},
	},
	{
		Name: "list_sections", Perm: "qa.read",
		Description: "List a project's case sections (a tree: each has its parent_id and full path).",
		Schema:      mcpObject([]string{"project_id"}, map[string]any{"project_id": mcpProject}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, _ mcpNoArgs) (any, error) {
			return qaOpListSections(c, call)
		}),
	},
	{
		Name: "list_cases", Perm: "qa.read",
		Description: "List a project's test cases, newest last, paged (default 100, at most 500). " +
			"Optionally only a section and its subsections, or titles containing query.",
		Schema: mcpObject([]string{"project_id"}, map[string]any{
			"project_id": mcpProject, "section_id": mcpString, "query": mcpString,
			"include_archived": map[string]any{"type": "boolean"},
			"limit":            map[string]any{"type": "integer", "minimum": 1, "maximum": qaAPIMaxLimit},
			"offset":           map[string]any{"type": "integer", "minimum": 0},
		}),
		Run: mcpProjectTool(qaOpListCases),
	},
	{
		Name: "create_cases", Perm: "qa.case.write",
		Description: "Create up to 500 test cases in one project, all or none. Each case names its section by path " +
			"(outermost first); missing sections are created. Returns the new refs (TC-n) in order.",
		Schema: mcpObject([]string{"project_id", "cases"}, map[string]any{
			"project_id": mcpProject,
			"cases": map[string]any{"type": "array", "minItems": 1, "maxItems": qaAPIMaxLimit, "items": mcpObject(
				[]string{"section_path", "title"}, map[string]any{
					"section_path": map[string]any{"type": "array", "items": mcpString, "minItems": 1},
					"title":        mcpString, "precondition": mcpString, "steps": mcpSteps,
					"priority": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
					"negative": map[string]any{"type": "boolean"}, "notes": mcpString,
				})},
		}),
		Run: mcpProjectTool(qaOpCreateCases),
	},
	{
		Name: "list_plans", Perm: "qa.read",
		Description: "List a project's test plans with their progress.",
		Schema:      mcpObject([]string{"project_id"}, map[string]any{"project_id": mcpProject}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, _ mcpNoArgs) (any, error) {
			return qaOpListPlans(c, call)
		}),
	},
	{
		Name: "list_runs", Perm: "qa.read",
		Description: "List the runs (test cycles) of a plan with their progress.",
		Schema:      mcpObject([]string{"project_id", "plan_id"}, map[string]any{"project_id": mcpProject, "plan_id": mcpString}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, a mcpPlanArgs) (any, error) {
			return qaOpListRuns(c, call, a.PlanID)
		}),
	},
	{
		Name: "get_run", Perm: "qa.read",
		Description: "Get a run with its items (one per case: steps, status, assignee), optionally only items in one status.",
		Schema: mcpObject([]string{"project_id", "run_id"}, map[string]any{
			"project_id": mcpProject, "run_id": mcpString,
			"status": map[string]any{"type": "string", "enum": qaItemStatuses},
		}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, a mcpRunArgs) (any, error) {
			return qaOpGetRun(c, call, a.RunID, a.Status)
		}),
	},
	{
		Name: "record_result", Perm: "qa.execute",
		Description: "Record the result of one run item: passed, failed, blocked or skipped, with an optional comment.",
		Schema: mcpObject([]string{"project_id", "item_id", "status"}, map[string]any{
			"project_id": mcpProject, "item_id": mcpString, "comment": mcpString,
			"status": map[string]any{"type": "string", "enum": []string{"passed", "failed", "blocked", "skipped"}},
		}),
		Run: mcpProjectTool(qaOpRecordResult),
	},
	{
		Name: "create_issue", Perm: "qa.issue.report",
		Description: "Report an issue (BUG-n). Give item_id to link it to the run item it was found in (its plan is then filled in).",
		Schema: mcpObject([]string{"project_id", "title", "severity"}, map[string]any{
			"project_id": mcpProject, "title": mcpString, "description": mcpString, "steps_to_reproduce": mcpString,
			"environment": mcpString, "plan_id": mcpString, "item_id": mcpString,
			"severity": map[string]any{"type": "string", "enum": []string{"blocker", "critical", "major", "minor", "trivial"}},
		}),
		Run: mcpProjectTool(qaOpCreateIssue),
	},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcpReply(c *fiber.Ctx, id json.RawMessage, result any, rpcErr *mcpRPCError) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := fiber.Map{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	return c.JSON(out)
}

// mcpToolError is a tool's failure as a result the assistant reads.
func mcpToolError(err error) fiber.Map {
	var rows *apiRowErrors
	msg := ""
	switch {
	case errors.As(err, &rows):
		msg = "some cases were refused; nothing was created:\n" + strings.Join(rows.rows, "\n")
	default:
		status, m := apiErrStatus(err)
		msg = m
		if status == fiber.StatusForbidden {
			msg = "this token lacks the permission for this tool (" + m + ")"
		}
	}
	return fiber.Map{"content": []fiber.Map{{"type": "text", "text": msg}}, "isError": true}
}

func qaMCP() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req mcpRequest
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			c.Status(fiber.StatusBadRequest)
			return mcpReply(c, nil, nil, &mcpRPCError{Code: -32700, Message: "parse error: send one JSON-RPC message"})
		}
		if req.Method == "" || len(req.ID) == 0 {
			// A notification or a response: accepted, nothing to answer.
			c.Status(fiber.StatusAccepted)
			return nil
		}
		p := mustPrincipal(c)
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(orEmpty(req.Params), &params)
			version := mcpProtocolVersions[0]
			if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
				version = params.ProtocolVersion
			}
			return mcpReply(c, req.ID, fiber.Map{
				"protocolVersion": version,
				"capabilities":    fiber.Map{"tools": fiber.Map{"listChanged": false}},
				"serverInfo":      fiber.Map{"name": "lotus-qa", "title": "Lotus QA", "version": "1"},
				"instructions":    mcpInstructions,
			}, nil)
		case "ping":
			return mcpReply(c, req.ID, fiber.Map{}, nil)
		case "tools/list":
			tools := []fiber.Map{}
			for _, t := range mcpTools {
				if p.Can(t.Perm) {
					tools = append(tools, fiber.Map{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
				}
			}
			return mcpReply(c, req.ID, fiber.Map{"tools": tools}, nil)
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(orEmpty(req.Params), &params); err != nil {
				return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32602, Message: "invalid params"})
			}
			i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == params.Name })
			if i < 0 {
				return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32602, Message: "unknown tool: " + params.Name})
			}
			tool := mcpTools[i]
			if !p.Can(tool.Perm) {
				return mcpReply(c, req.ID, mcpToolError(apiErr(fiber.StatusForbidden, "missing permission: %s", tool.Perm)), nil)
			}
			out, err := tool.Run(c, params.Arguments)
			if err != nil {
				return mcpReply(c, req.ID, mcpToolError(err), nil)
			}
			text, _ := json.Marshal(out)
			return mcpReply(c, req.ID, fiber.Map{
				"content":           []fiber.Map{{"type": "text", "text": string(text)}},
				"structuredContent": out,
			}, nil)
		default:
			return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
}

func registerQAMCPRoutes(app *fiber.App, authDeps authn.Deps) {
	app.Post("/mcp/qa", append(qaAPIChain(authDeps, "qa.read"), qaMCP())...)
}
```

Apply to `internal/web/server.go`:

```diff
--- a/internal/web/server.go
+++ b/internal/web/server.go
@@ -191,6 +191,7 @@ func New(d Deps) *fiber.App {
 
 	registerIngestRoutes(app, d, authDeps)
 	registerQAAPIRoutes(app, authDeps)
+	registerQAMCPRoutes(app, authDeps)
 
 	return app
 }
```

Apply to `internal/web/settings_tokens.go`:

```diff
--- a/internal/web/settings_tokens.go
+++ b/internal/web/settings_tokens.go
@@ -133,6 +133,12 @@ func renderTokensPage(c *fiber.Ctx, d Deps, plain, label, errMsg string, status
 			serviceRows = append(serviceRows, buildTokenRow(t))
 		}
 	}
+	// Where the QA API and its MCP endpoint answer: this organization's
+	// own origin.
+	qaOrigin := ""
+	if o, ok := tenant.OrgFromContext(c); ok {
+		qaOrigin = d.Cfg.TenantURL(o.Subdomain)
+	}
 	nav := buildNav("", len(projects))
 	nav.Trail = []httpx.NavCrumb{{Label: "Settings"}}
 	c.Type("html")
@@ -147,8 +153,9 @@ func renderTokensPage(c *fiber.Ctx, d Deps, plain, label, errMsg string, status
 			NewTokenPlain string
 			NewTokenLabel string
 			Error         string
+			QAOrigin      string
 		}{SettingsNav: buildSettingsNav("tokens"), PATs: patRows, ServiceTokens: serviceRows, CanManageOrg: canManageOrg,
-			NewTokenPlain: plain, NewTokenLabel: label, Error: errMsg},
+			NewTokenPlain: plain, NewTokenLabel: label, Error: errMsg, QAOrigin: qaOrigin},
 	})
 }
```

Apply to `web/templates/pages/settings_tokens.html`:

```diff
--- a/web/templates/pages/settings_tokens.html
+++ b/web/templates/pages/settings_tokens.html
@@ -42,6 +42,17 @@
   </div>
 {{template "lotus_card_section_close" .}}
 
+{{template "lotus_card_section_open" (dict "Padding" "12px 14px" "Style" "margin-bottom:16px;display:flex;flex-direction:column;gap:8px")}}
+  <div style="font-size:13px;font-weight:600;color:#43284E">QA API and AI assistants (MCP)</div>
+  <p style="margin:0;font-size:12px;color:#6B7280">A token reaches QA with what both its scopes and your role allow. Send it as <code>Authorization: Bearer &lt;token&gt;</code>; the browser session does not work here.</p>
+  <div style="display:grid;grid-template-columns:90px minmax(0,1fr);gap:6px 10px;font-size:12px;align-items:baseline">
+    <span style="color:#6B7280">JSON API</span><code style="font:400 12px 'IBM Plex Mono',monospace;word-break:break-all;user-select:all">{{.Data.QAOrigin}}/api/qa/projects</code>
+    <span style="color:#6B7280">MCP server</span><code style="font:400 12px 'IBM Plex Mono',monospace;word-break:break-all;user-select:all">{{.Data.QAOrigin}}/mcp/qa</code>
+  </div>
+  <pre style="margin:0;padding:9px 11px;background:#F9FAFB;border-radius:6px;font:400 11.5px 'IBM Plex Mono',monospace;color:#374151;overflow-x:auto">{"mcpServers": {"lotus-qa": {"type": "http", "url": "{{.Data.QAOrigin}}/mcp/qa",
+  "headers": {"Authorization": "Bearer &lt;token&gt;"}}}}</pre>
+{{template "lotus_card_section_close" .}}
+
 {{if .Data.CanManageOrg}}
 {{template "lotus_card_section_open" (dict "Padding" "4px" "Style" "overflow:hidden")}}
   <div style="display:flex;align-items:center;gap:10px;padding:10px 14px;border-bottom:1px solid #F3F4F6">
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/web -run 'TestQAMCP' -v`
Expected: PASS.

Then: `go test ./... && make lint`
Expected: PASS, `check-repo-scope: ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/web/qa_mcp.go internal/web/qa_mcp_test.go internal/web/server.go internal/web/settings_tokens.go web/templates/pages/settings_tokens.html
git commit -m "feat(qa): MCP endpoint over the QA API; Settings shows how to connect"
```

---

## Self-review

1. **Outline coverage.**
   - 8.1: Tasks 1–2 cover every listed operation: projects, sections, cases (paged), bulk create (at most 500, section by path), scenarios, plans, runs, items, record result and create issue. Their tests check that a token narrower than its role is enforced (Task 1 test and the read-only token in Task 2), that bulk create is atomic, and that keys are sequential under concurrent bulk calls.
   - 8.2: Task 3 provides the tool list (the outline's eight plus `list_runs`). Each tool maps to its API operation with the same permission, and an unauthenticated call is 401.
2. **Placeholders.** None: every step carries the file or the exact change, taken from a commit that passed on its own.
3. **Type consistency.** Task 3 calls the operations Tasks 1–2 define (`qaOpListCases(c, call, qaCasesArgs)`, `qaOpCreateCases(c, call, qaCreateCasesArgs)`, `qaOpRecordResult(c, call, qaRecordResultArgs)`, `qaOpCreateIssue(c, call, qaCreateIssueArgs)`). This was verified by building each task's commit alone.
4. **Review Focus.** Each line names the tasks whose tests pin it.
