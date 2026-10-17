package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// seedQALibrary gives a project one section with two cases, a scenario
// holding both, an open plan with one run, and one open issue.
func seedQALibrary(t *testing.T, e qaEnv, projectID uuid.UUID) (sectionID uuid.UUID, cases []*repo.QACase) {
	t.Helper()
	ctx := context.Background()
	sec, err := repo.CreateQASection(ctx, e.s, projectID, "Auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Login", "Logout"} {
		c, err := repo.CreateQACase(ctx, e.s, projectID, repo.QACaseInput{SectionID: sec.ID, Title: title}, nil)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c)
	}
	sc, err := repo.CreateQAScenario(ctx, e.s, projectID, "Regression core", "regression", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddQAScenarioCases(ctx, e.s, sc.ID, nil, []uuid.UUID{cases[0].ID, cases[1].ID}); err != nil {
		t.Fatal(err)
	}
	plan, err := repo.CreateQAPlan(ctx, e.s, projectID, repo.QAPlanInput{Name: "Regression October", Phase: "regression", ScenarioIDs: []uuid.UUID{sc.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateQARun(ctx, e.s, plan.ID, repo.QARunInput{Name: "Cycle 1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateQAIssue(ctx, e.s, projectID, repo.QAIssueInput{Title: "Submit returns 500", Severity: "major", Source: "internal"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return sec.ID, cases
}

func TestQAProject_LandingShowsCountsAndSubnav(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qaproj")
	p := e.project(t, "Portal")
	seedQALibrary(t, e, p)
	base := e.projectPath(t, p) + "/qa"

	status, body := e.get(t, e.owner, base)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for _, want := range []string{
		`data-qa-count="cases">2<`, `data-qa-count="scenarios">1<`, `data-qa-count="plans">1<`, `data-qa-count="issues">1<`,
		`href="` + base + `/cases"`, `href="` + base + `/scenarios"`,
		`href="/projects/Portal"`, // breadcrumb back to the project, by slug
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("landing missing %q:\n%s", want, body)
		}
	}
}

func TestQAProject_GuestReachesOnlyItsProject(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qaproj-guest")
	mine := e.project(t, "Client portal")
	other := e.project(t, "Internal billing")
	guest, guestID := e.session(t, "client@bank.example", "guest")
	if err := repo.AddProjectMember(context.Background(), e.s, mine, guestID); err != nil {
		t.Fatal(err)
	}

	if status, _ := e.get(t, guest, e.projectPath(t, other)+"/qa"); status != http.StatusNotFound {
		t.Fatalf("guest on another project = %d; want 404", status)
	}
	if status, _ := e.get(t, guest, "/projects/"+uuid.NewString()+"/qa"); status != http.StatusNotFound {
		t.Fatalf("guest on unknown project = %d; want 404", status)
	}
	status, body := e.get(t, guest, e.projectPath(t, mine)+"/qa")
	if status != http.StatusOK {
		t.Fatalf("guest on its project = %d", status)
	}
	// A guest has no dashboard.read: the breadcrumb leads back to QA Tools,
	// never to the project's metrics page.
	if !strings.Contains(body, `href="/qa"`) || strings.Contains(body, `href="`+e.projectPath(t, mine)+`"`) {
		t.Fatalf("guest breadcrumb wrong:\n%s", body)
	}
}

func TestProjectPage_LinksToTestCases(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qaproj-link")
	p := e.project(t, "Portal")
	_, body := e.get(t, e.owner, e.projectPath(t, p))
	if !strings.Contains(body, `href="/projects/Portal/qa"`) || !strings.Contains(body, "Test cases") {
		t.Fatalf("project page has no Test cases link:\n%s", body)
	}
}

func TestProjectDelete_CountsAndConfirmsQARows(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qaproj-del")
	p := e.project(t, "Portal")
	seedQALibrary(t, e, p)
	// The JSON endpoints keep taking the project's id.
	path := "/projects/" + p.String()

	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, path+"/delete-impact", e.owner), -1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var impact map[string]int
	if err := json.Unmarshal(raw, &impact); err != nil {
		t.Fatalf("impact %s: %v", raw, err)
	}
	if impact["QACases"] != 2 || impact["QARuns"] != 1 || impact["QAIssues"] != 1 {
		t.Fatalf("impact = %s", raw)
	}

	// Confirming only the metric counts no longer covers what would go.
	stale, _ := e.app.Test(reqWithSession(http.MethodDelete, e.host, path+"?confirm_components=0&confirm_bindings=0", e.owner), -1)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("delete without QA confirmation = %d; want 409", stale.StatusCode)
	}
	ok, _ := e.app.Test(reqWithSession(http.MethodDelete, e.host,
		path+"?confirm_components=0&confirm_bindings=0&confirm_qa_cases=2&confirm_qa_runs=1&confirm_qa_issues=1", e.owner), -1)
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("confirmed delete = %d; want 204", ok.StatusCode)
	}
}
