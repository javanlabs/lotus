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
	t.Parallel()
	e := newQAAPIEnv(t, "acme-api-auth")

	status, body := e.api(t, http.MethodGet, "", "/api/qa/projects", nil)
	if status != http.StatusUnauthorized || decodeJSON[map[string]string](t, body)["error"] == "" {
		t.Fatalf("no token = %d %s; want 401 with a JSON error", status, body)
	}
	// A browser session is not an API credential: no redirect to sign in,
	// no ambient cookie authority.
	req := reqWithSession(http.MethodGet, e.host, "/api/qa/projects", e.member)
	resp, err := e.app.Test(req, -1)
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
	t.Parallel()
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
	t.Parallel()
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
