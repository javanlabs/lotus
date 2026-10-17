package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// runFixture: a project with the seeded library, a member, a guest listed
// on the project, and the plan.
type qaRunEnv struct {
	qaEnv
	projectID uuid.UUID
	plan      repo.QAPlan
	member    string
	memberID  uuid.UUID
	guest     string
	guestID   uuid.UUID
	base      string
}

func newQARunEnv(t *testing.T, sub string) qaRunEnv {
	t.Helper()
	return newQARunEnvWith(t, sub, nil)
}

func newQARunEnvWith(t *testing.T, sub string, change func(*Deps)) qaRunEnv {
	t.Helper()
	e := newQAEnvWith(t, sub, change)
	p := e.project(t, "Portal")
	seedQALibrary(t, e, p)
	r := qaRunEnv{qaEnv: e, projectID: p, plan: planNamed(t, e, p, "Regression October"), base: e.projectPath(t, p) + "/qa"}
	r.member, r.memberID = e.session(t, "qa@acme.example", "member")
	r.guest, r.guestID = e.session(t, "client@bank.example", "guest")
	if err := repo.AddProjectMember(context.Background(), e.s, p, r.guestID); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestQARunNew_CreatesWithDistribution(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-runnew")
	ctx := context.Background()
	path := r.base + "/plans/" + r.plan.ID.String() + "/runs"

	status, body := r.get(t, r.member, path+"/new")
	if status != http.StatusOK || !strings.Contains(body, "client@bank.example") || !strings.Contains(body, "qa@acme.example") {
		t.Fatalf("new run page = %d; testers should list members and the project's guest", status)
	}
	form := url.Values{"name": {"Cycle 2"}, "tester_id": {r.memberID.String(), r.guestID.String()}, "distribution": {"round_robin"}}
	resp, _ := r.post(t, r.member, path, form, false)
	runs, _ := repo.ListQARuns(ctx, r.s, r.plan.ID)
	run := runs[len(runs)-1]
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != r.base+"/runs/"+run.ID.String() || run.Name != "Cycle 2" || run.Cycle != 2 {
		t.Fatalf("create = %d %s, %+v", resp.StatusCode, resp.Header.Get("Location"), run)
	}
	items, _ := repo.ListQARunItems(ctx, r.s, run.ID, repo.QAItemFilter{})
	if len(items) != 2 || items[0].AssigneeID == nil || *items[0].AssigneeID != r.memberID || *items[1].AssigneeID != r.guestID {
		t.Fatalf("round robin items = %+v", items)
	}
}

func TestQARunNew_PreviewAndRejections(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-runnew-bad")
	ctx := context.Background()
	viewer, viewerID := r.session(t, "viewer@acme.example", "viewer")
	path := r.base + "/plans/" + r.plan.ID.String() + "/runs"

	_, body := r.post(t, r.member, path+"/preview", url.Values{"tester_id": {r.memberID.String()}, "distribution": {"round_robin"}}, true)
	if !strings.Contains(body, "2 cases") || !strings.Contains(body, `data-preview-count="2"`) {
		t.Fatalf("preview:\n%s", body)
	}
	_, body = r.post(t, r.member, path+"/preview", url.Values{"distribution": {"by_group"}}, true)
	if !strings.Contains(body, "Choose at least one tester to share the cases between.") {
		t.Fatalf("by group without testers:\n%s", body)
	}

	// A viewer cannot execute, so it is not a tester; nor is a stranger.
	for _, id := range []uuid.UUID{viewerID, uuid.New()} {
		resp, _ := r.post(t, r.member, path, url.Values{"name": {"X"}, "tester_id": {id.String()}}, false)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("tester %s = %d; want 404", id, resp.StatusCode)
		}
	}
	if resp, _ := r.post(t, r.guest, path, url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest create = %d; want 403", resp.StatusCode)
	}
	if resp, _ := r.post(t, viewer, path, url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create = %d; want 403", resp.StatusCode)
	}

	empty, _ := repo.CreateQAPlan(ctx, r.s, r.projectID, repo.QAPlanInput{Name: "Empty", Phase: "sit"}, nil)
	resp, body := r.post(t, r.member, r.base+"/plans/"+empty.ID.String()+"/runs", url.Values{"name": {"X"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "The run would have no cases.") {
		t.Fatalf("empty run = %d", resp.StatusCode)
	}
	repo.CloseQAPlan(ctx, r.s, r.plan.ID)
	resp, body = r.post(t, r.member, path, url.Values{"name": {"X"}}, false)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This plan is closed.") {
		t.Fatalf("closed plan = %d", resp.StatusCode)
	}
}
