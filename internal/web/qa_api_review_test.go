package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authn"
	"github.com/uyab/lotus/internal/repo"
)

// A service token pinned to a component (an ingest credential) is not a QA
// credential for the whole organization: it reaches no project.
func TestQAAPI_ComponentPinnedTokenReachesNoProject(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-component-pin")
	comp, err := repo.CreateComponent(context.Background(), e.s, repo.NewComponent{ProjectID: e.projectID, Slug: "web", Name: "Web", Kind: "service"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := authn.IssueServiceToken(context.Background(), authn.Deps{DB: e.db}, authn.NewServiceTokenParams{
		OrgID: e.orgID, ActorID: e.memberID, ActorRole: "admin", Name: "ingest", Scopes: []string{"qa.read"}, ComponentID: &comp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := e.api(t, http.MethodGet, raw, "/api/qa/projects", nil); strings.Contains(body, e.projectID.String()) || strings.Contains(body, e.other.String()) {
		t.Errorf("component-pinned token lists projects: %s", body)
	}
	for _, p := range []string{e.projectID.String(), e.other.String()} {
		if status, _ := e.api(t, http.MethodGet, raw, "/api/qa/projects/"+p+"/cases", nil); status != http.StatusNotFound {
			t.Errorf("component-pinned token on a project = %d; want 404", status)
		}
	}
}

// An error the API does not expect is a 500 to the caller and a line in the
// log with what went wrong; the caller never sees the internals.
func TestQAAPI_UnexpectedErrorsAreLogged(t *testing.T) {
	var logged []string
	prev := qaAPILogf
	qaAPILogf = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	defer func() { qaAPILogf = prev }()

	app := fiber.New()
	app.Get("/boom", apiJSONErrors, func(c *fiber.Ctx) error { return errors.New("pq: connection reset") })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/boom", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError || len(logged) != 1 || !strings.Contains(logged[0], "connection reset") || !strings.Contains(logged[0], "/boom") {
		t.Fatalf("status %d, log %q", resp.StatusCode, logged)
	}
	logged = nil
	app = fiber.New()
	app.Get("/tool", func(c *fiber.Ctx) error { return c.JSON(mcpToolError(c, errors.New("pq: connection reset"))) })
	resp, _ = app.Test(httptest.NewRequest(http.MethodGet, "/tool", nil))
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if len(logged) != 1 || strings.Contains(string(buf[:n]), "connection reset") {
		t.Fatalf("MCP tool error: log %q, body %s", logged, buf[:n])
	}
}

// NUL cannot be stored as text; it is refused as input, never a 500.
func TestQAAPI_NULInInputIsRefused(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-nul")
	base := "/api/qa/projects/" + e.projectID.String()
	rows := apiCaseRows(2, "Auth")
	rows[1]["steps"] = []map[string]string{{"action": "a\u0000b"}}
	if status, body := e.api(t, http.MethodPost, e.memberToken, base+"/cases", map[string]any{"cases": rows}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Row 2") {
		t.Errorf("NUL in a step = %d %s; want 422 naming row 2", status, body)
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/cases", map[string]any{"cases": apiCaseRows(1, "Au\u0000th")}); status != http.StatusUnprocessableEntity {
		t.Errorf("NUL in a section = %d; want 422", status)
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/issues", map[string]string{"title": "x\u0000", "severity": "minor"}); status >= 500 {
		t.Errorf("NUL in an issue title = %d; want 4xx", status)
	}
	_, items := e.firstRun(t)
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/items/"+items[0].ID.String()+"/result", map[string]string{"status": "passed", "comment": "\u0000"}); status >= 500 {
		t.Errorf("NUL in a comment = %d; want 4xx", status)
	}
	res := e.callTool(t, e.memberToken, "create_issue", map[string]any{"project_id": e.projectID.String(), "title": "a\u0000", "severity": "minor"})
	if !res.IsError || strings.Contains(res.Content[0].Text, "internal") {
		t.Errorf("MCP NUL title = %+v; want a refusal, not an internal error", res)
	}
}

// The API reads bodies as strictly as MCP reads arguments: a misspelled
// field is an error, not 500 cases created without it.
func TestQAAPI_UnknownFieldsAreRefused(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-strict")
	base := "/api/qa/projects/" + e.projectID.String()
	rows := apiCaseRows(1, "Auth")
	rows[0]["expected"] = "misplaced"
	if status, body := e.api(t, http.MethodPost, e.memberToken, base+"/cases", map[string]any{"cases": rows}); status != http.StatusBadRequest || !strings.Contains(body, "expected") {
		t.Errorf("unknown case field = %d %s; want 400 naming it", status, body)
	}
	if status, _ := e.api(t, http.MethodPost, e.memberToken, base+"/issues", map[string]string{"title": "x", "severity": "minor", "priority": "high"}); status != http.StatusBadRequest {
		t.Errorf("unknown issue field = %d; want 400", status)
	}
	if n, _ := repo.CountQACases(context.Background(), e.s, e.projectID, repo.QACaseFilter{}); n != 2 {
		t.Errorf("cases = %d after refused bodies; want 2", n)
	}
}

// A run is read in pages, and its case refs come from its own cases.
func TestQAAPI_GetRunIsPaged(t *testing.T) {
	e := newQAAPIEnv(t, "acme-api-run-page")
	run, _ := e.firstRun(t)
	path := "/api/qa/projects/" + e.projectID.String() + "/runs/" + run.ID.String()
	status, body := e.api(t, http.MethodGet, e.readToken, path+"?limit=1&offset=1", nil)
	got := decodeJSON[struct {
		Total int `json:"total"`
		Items []struct {
			CaseRef string `json:"case_ref"`
		} `json:"items"`
		Progress struct{ Total int } `json:"progress"`
	}](t, body)
	if status != http.StatusOK || got.Total != 2 || len(got.Items) != 1 || got.Items[0].CaseRef != "TC-2" || got.Progress.Total != 2 {
		t.Fatalf("page 2 of the run = %d %s", status, body)
	}
	if status, _ := e.api(t, http.MethodGet, e.readToken, path+"?limit=501", nil); status != http.StatusBadRequest {
		t.Errorf("limit 501 = %d; want 400", status)
	}
	res := e.callTool(t, e.readToken, "get_run", map[string]any{"project_id": e.projectID.String(), "run_id": run.ID.String(), "limit": 1})
	if res.IsError || len(res.StructuredContent["items"].([]any)) != 1 {
		t.Errorf("get_run tool with limit = %+v", res)
	}
}

// A token is revoked only by its owner, or (a service token) by an admin of
// its organization.
func TestTokens_RevokeOnlyYourOwn(t *testing.T) {
	e := newQAAPIEnv(t, "acme-token-revoke")
	other := newQAEnvOnApp(t, e.qaEnv, "other-token-revoke")
	var ids []string
	for _, owner := range []struct {
		where string
		id    any
	}{{"user_id", e.memberID}, {"org_id", e.orgID}} {
		var id string
		if err := e.db.Get(&id, `SELECT id FROM api_tokens WHERE `+owner.where+` = $1 AND revoked_at IS NULL LIMIT 1`, owner.id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if resp, _ := other.post(t, other.owner, "/settings/tokens/"+id+"/revoke", url.Values{}, false); resp.StatusCode != http.StatusNotFound {
			t.Errorf("another organization's owner revoking %s = %d; want 404", id, resp.StatusCode)
		}
	}
	guestless, _ := e.session(t, "viewer@acme.example", "viewer")
	if resp, _ := e.post(t, guestless, "/settings/tokens/"+ids[0]+"/revoke", url.Values{}, false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a colleague revoking a personal token = %d; want 404", resp.StatusCode)
	}
	var revoked int
	if err := e.db.Get(&revoked, `SELECT count(*) FROM api_tokens WHERE revoked_at IS NOT NULL`); err != nil || revoked != 0 {
		t.Fatalf("%d tokens revoked by people who do not own them (%v)", revoked, err)
	}
}
