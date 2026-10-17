package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/mailer"
	"github.com/uyab/lotus/internal/repo"
)

// sentMail replaces qaSendMail for the test and returns what it was given.
func sentMail(t *testing.T) func() []mailer.Message {
	t.Helper()
	var mu sync.Mutex
	var sent []mailer.Message
	prev := qaSendMail
	qaSendMail = func(_ *mailer.Mailer, msg mailer.Message) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, msg)
		return nil
	}
	t.Cleanup(func() { qaSendMail = prev })
	return func() []mailer.Message {
		mu.Lock()
		defer mu.Unlock()
		return append([]mailer.Message(nil), sent...)
	}
}

// newQARunMailEnv is newQARunEnv with a configured base domain, so the
// links in notification emails are absolute.
func newQARunMailEnv(t *testing.T, sub string) qaRunEnv {
	t.Helper()
	// Sets the one field it needs rather than replacing Cfg: a whole new
	// Config would also switch LOTUS_FEATURE_QA back off and unregister the
	// very routes this test drives.
	return newQARunEnvWith(t, sub, func(d *Deps) { d.Cfg.BaseDomain = dashboardTestDomain })
}

func TestQARun_ResultCommentLimit(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-comment")
	ctx := context.Background()
	run, items := r.firstRun(t)
	long := strings.Repeat("é", repo.MaxQACommentLength+1)

	_, body := r.record(t, r.member, run.ID, items[0].ID, "failed", long)
	if !strings.Contains(body, fmt.Sprintf("A comment can be at most %d characters.", repo.MaxQACommentLength)) {
		t.Fatalf("long comment in the panel:\n%s", body)
	}
	if got, _ := repo.GetQARunItem(ctx, r.s, items[0].ID); got.Status != "untested" {
		t.Fatalf("a refused comment changed the status to %s", got.Status)
	}
	if !strings.Contains(body, fmt.Sprintf(`name="comment" rows="2" maxlength="%d"`, repo.MaxQACommentLength)) {
		t.Fatal("the comment box does not carry the limit")
	}

	// The API and the MCP tool refuse it the same way, as a validation
	// error.
	api := newQAAPIEnv(t, "acme-qarun-comment-api")
	_, apiItems := api.firstRun(t)
	path := "/api/qa/projects/" + api.projectID.String() + "/items/" + apiItems[0].ID.String() + "/result"
	status, body := api.api(t, http.MethodPost, api.memberToken, path, map[string]string{"status": "failed", "comment": long})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, fmt.Sprintf("comment can be at most %d characters", repo.MaxQACommentLength)) {
		t.Fatalf("API long comment = %d %s", status, body)
	}
	res := api.callTool(t, api.memberToken, "record_result", map[string]any{
		"project_id": api.projectID.String(), "item_id": apiItems[0].ID.String(), "status": "failed", "comment": long,
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "at most 2500 characters") {
		t.Fatalf("MCP long comment = %+v", res)
	}
	if got, _ := repo.GetQARunItem(ctx, api.s, apiItems[0].ID); got.Status != "untested" {
		t.Fatalf("a refused API comment changed the status to %s", got.Status)
	}
}

func TestQAAPI_ItemsCarryTheirLatestComment(t *testing.T) {
	t.Parallel()
	e := newQAAPIEnv(t, "acme-api-lastcomment")
	ctx := context.Background()
	run, items := e.firstRun(t)
	project := e.projectID.String()
	base := "/api/qa/projects/" + project

	status, body := e.api(t, http.MethodPost, e.memberToken, base+"/items/"+items[0].ID.String()+"/result",
		map[string]string{"status": "failed", "comment": "  500 on submit "})
	reply := decodeJSON[struct {
		Item apiRunItem `json:"item"`
	}](t, body)
	if status != http.StatusOK || reply.Item.LastComment == nil || reply.Item.LastComment.Text != "500 on submit" ||
		reply.Item.LastComment.By == nil || *reply.Item.LastComment.By != e.memberID {
		t.Fatalf("record_result reply = %d %s", status, body)
	}

	// A later blank result is the latest: its comment is null, not the
	// earlier one's.
	repo.RecordQAResult(ctx, e.s, repo.QAResultInput{ItemID: items[1].ID, Status: "blocked", Comment: "No data", CreatedBy: &e.memberID})
	repo.RecordQAResult(ctx, e.s, repo.QAResultInput{ItemID: items[1].ID, Status: "passed", CreatedBy: &e.memberID})
	_, body = e.api(t, http.MethodGet, e.memberToken, base+"/runs/"+run.ID.String(), nil)
	got := decodeJSON[struct {
		Items []apiRunItem `json:"items"`
	}](t, body)
	if len(got.Items) != 2 || got.Items[0].LastComment == nil || got.Items[0].LastComment.Text != "500 on submit" || got.Items[1].LastComment != nil {
		t.Fatalf("get_run items = %s", body)
	}
	if !strings.Contains(body, `"last_comment":null`) {
		t.Fatalf("a blank latest comment is not null: %s", body)
	}

	res := e.callTool(t, e.memberToken, "get_run", map[string]any{"project_id": project, "run_id": run.ID.String()})
	first := res.StructuredContent["items"].([]any)[0].(map[string]any)
	if lc, ok := first["last_comment"].(map[string]any); !ok || lc["text"] != "500 on submit" {
		t.Fatalf("MCP get_run item = %+v", first)
	}
}

func TestQARun_EditSyncsAssigneeAndNotifies(t *testing.T) {
	r := newQARunMailEnv(t, "acme-qarun-edit")
	ctx := context.Background()
	mail := sentMail(t)
	run, items := r.firstRun(t)
	_, otherID := r.session(t, "lead@acme.example", "member")
	path := r.base + "/runs/" + run.ID.String()
	// The first item follows the run (nobody); the second is given to the guest.
	repo.AssignQARunItems(ctx, r.s, run.ID, []uuid.UUID{items[1].ID}, &r.guestID)

	if _, body := r.get(t, r.member, path); !strings.Contains(body, "Edit run…") || !strings.Contains(body, `maxlength="2000"`) {
		t.Fatal("no edit form on the run page")
	}
	resp, _ := r.post(t, r.member, path, url.Values{"name": {"Cycle 1 (rc2)"}, "notes": {"Build rc2"}, "assignee_id": {otherID.String()}}, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("edit = %d", resp.StatusCode)
	}
	got, _ := repo.GetQARun(ctx, r.s, run.ID)
	if got.Name != "Cycle 1 (rc2)" || got.Notes != "Build rc2" || got.AssigneeID == nil || *got.AssigneeID != otherID {
		t.Fatalf("run after edit = %+v", got)
	}
	after, _ := repo.ListQARunItems(ctx, r.s, run.ID, repo.QAItemFilter{})
	if after[0].AssigneeID == nil || *after[0].AssigneeID != otherID || *after[1].AssigneeID != r.guestID {
		t.Fatalf("items after edit: %v %v; want the lead's, then the guest's", after[0].AssigneeID, after[1].AssigneeID)
	}
	sent := mail()
	if len(sent) != 1 || sent[0].To.Email != "lead@acme.example" || !strings.Contains(sent[0].Subject, "Cycle 1 (rc2)") {
		t.Fatalf("mail = %+v; want one to the new assignee", sent)
	}
	link := "http://acme-qarun-edit." + dashboardTestDomain + path
	if !strings.Contains(sent[0].HTML, `href="`+link+`"`) || !strings.Contains(sent[0].HTML, "Regression October") ||
		!strings.Contains(sent[0].HTML, "Portal") || !strings.Contains(sent[0].HTML, "1 item") {
		t.Fatalf("mail body:\n%s", sent[0].HTML)
	}
	if _, body := r.get(t, r.member, path); !strings.Contains(body, "lead@acme.example") {
		t.Fatal("the run page does not show its assignee")
	}
	if _, body := r.get(t, r.member, r.base+"/plans/"+r.plan.ID.String()); !strings.Contains(body, "lead@acme.example") {
		t.Fatal("the plan's run list does not show the assignee")
	}

	// Renaming alone sends nothing and moves nothing; assigning oneself
	// sends nothing either.
	r.post(t, r.member, path, url.Values{"name": {"Cycle 1"}, "assignee_id": {otherID.String()}}, true)
	r.post(t, r.member, path, url.Values{"name": {"Cycle 1"}, "assignee_id": {r.memberID.String()}}, true)
	if n := len(mail()); n != 1 {
		t.Fatalf("mails after rename and self-assign = %d; want still 1", n)
	}

	_, body := r.post(t, r.member, path, url.Values{"name": {"Cycle 1"}, "notes": {strings.Repeat("x", repo.MaxQARunNotesLength+1)}}, true)
	if !strings.Contains(body, fmt.Sprintf("Notes can be at most %d characters.", repo.MaxQARunNotesLength)) {
		t.Fatalf("long notes:\n%s", body)
	}
	_, body = r.post(t, r.member, path, url.Values{"name": {" "}}, true)
	if !strings.Contains(body, "Run name is required") {
		t.Fatalf("blank name:\n%s", body)
	}
	_, viewerID := r.session(t, "viewer@acme.example", "viewer")
	if resp, _ := r.post(t, r.member, path, url.Values{"name": {"X"}, "assignee_id": {viewerID.String()}}, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("assign the run to a viewer = %d; want 404", resp.StatusCode)
	}
	if resp, _ := r.post(t, r.guest, path, url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest edit = %d; want 403", resp.StatusCode)
	}
	repo.CloseQARun(ctx, r.s, run.ID)
	if resp, _ := r.post(t, r.member, path, url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusConflict {
		t.Fatalf("edit a closed run = %d; want 409", resp.StatusCode)
	}
}

func TestQARunNew_AssigneeTakesTheRestAndIsNotified(t *testing.T) {
	r := newQARunMailEnv(t, "acme-runnew-lead")
	ctx := context.Background()
	mail := sentMail(t)
	path := r.base + "/plans/" + r.plan.ID.String() + "/runs"

	if _, body := r.get(t, r.member, path+"/new"); !strings.Contains(body, `name="assignee_id"`) || !strings.Contains(body, `maxlength="2000"`) {
		t.Fatal("the new run form has no assignee or notes limit")
	}
	resp, _ := r.post(t, r.member, path, url.Values{"name": {"Cycle 2"}, "assignee_id": {r.guestID.String()}}, false)
	runs, _ := repo.ListQARuns(ctx, r.s, r.plan.ID)
	run := runs[len(runs)-1]
	if resp.StatusCode != http.StatusFound || run.AssigneeID == nil || *run.AssigneeID != r.guestID {
		t.Fatalf("create = %d, %+v", resp.StatusCode, run)
	}
	if _, per, _ := repo.QARunProgress(ctx, r.s, run.ID); per[r.guestID].Total != 2 {
		t.Fatalf("the run's assignee holds %+v; want both items", per)
	}
	if sent := mail(); len(sent) != 1 || sent[0].To.Email != "client@bank.example" || !strings.Contains(sent[0].HTML, "2 items") {
		t.Fatalf("mail = %+v", sent)
	}
	// Taking the run oneself sends nothing.
	r.post(t, r.member, path, url.Values{"name": {"Cycle 3"}, "assignee_id": {r.memberID.String()}}, false)
	if n := len(mail()); n != 1 {
		t.Fatalf("mails = %d; want still 1", n)
	}
	resp, body := r.post(t, r.member, path, url.Values{"name": {"Cycle 4"}, "notes": {strings.Repeat("x", repo.MaxQARunNotesLength+1)}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "Notes can be at most 2000 characters.") {
		t.Fatalf("long notes = %d", resp.StatusCode)
	}
	if resp, _ := r.post(t, r.member, path, url.Values{"name": {"X"}, "assignee_id": {uuid.NewString()}}, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a stranger as assignee = %d; want 404", resp.StatusCode)
	}
}

// itemWithIssue records a failure on the run's first item and reports an
// issue from it.
func itemWithIssue(t *testing.T, r qaRunEnv) (repo.QARun, []repo.QARunItem, *repo.QAIssue) {
	t.Helper()
	ctx := context.Background()
	run, items := r.firstRun(t)
	if _, err := repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", Comment: "500"}); err != nil {
		t.Fatal(err)
	}
	is, err := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{Title: "Submit fails", Severity: "major", Source: "internal"}, nil, &items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, items, is
}

func TestQARun_RemoveItemsKeepsIssues(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-remove")
	ctx := context.Background()
	run, items, is := itemWithIssue(t, r)
	path := r.base + "/runs/" + run.ID.String()

	if _, body := r.get(t, r.member, path); !strings.Contains(body, path+"/items/remove") {
		t.Fatal("no remove control on the run page")
	}
	_, body := r.post(t, r.member, path+"/items/remove", nil, true)
	if !strings.Contains(body, "Select at least one item.") {
		t.Fatalf("remove nothing:\n%s", body)
	}
	resp, _ := r.post(t, r.member, path+"/items/remove", url.Values{"item_id": {items[0].ID.String()}}, true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %d", resp.StatusCode)
	}
	if left, _ := repo.ListQARunItems(ctx, r.s, run.ID, repo.QAItemFilter{}); len(left) != 1 {
		t.Fatalf("items left = %d; want 1", len(left))
	}
	if status, _ := r.get(t, r.member, fmt.Sprintf("%s/issues/%d", r.base, is.Key)); status != http.StatusOK {
		t.Fatalf("issue after its item was removed = %d; want it kept", status)
	}
	if n := auditCount(t, r.db, r.orgID, "qa.run.items.remove"); n != 1 {
		t.Fatalf("qa.run.items.remove audit rows = %d; want 1", n)
	}
	if resp, _ := r.post(t, r.guest, path+"/items/remove", url.Values{"item_id": {items[1].ID.String()}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest remove = %d; want 403", resp.StatusCode)
	}
	repo.CloseQARun(ctx, r.s, run.ID)
	if resp, _ := r.post(t, r.member, path+"/items/remove", url.Values{"item_id": {items[1].ID.String()}}, false); resp.StatusCode != http.StatusConflict {
		t.Fatalf("remove from a closed run = %d; want 409", resp.StatusCode)
	}
}

func TestQARun_DeleteKeepsIssuesAndOpensThePlan(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qarun-delete")
	ctx := context.Background()
	run, _, is := itemWithIssue(t, r)
	path := r.base + "/runs/" + run.ID.String()
	repo.CloseQARun(ctx, r.s, run.ID) // closed runs can be deleted too

	if _, body := r.get(t, r.member, path); !strings.Contains(body, "Delete run…") {
		t.Fatal("no delete control on a closed run's page")
	}
	if resp, _ := r.post(t, r.guest, path+"/delete", nil, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("guest delete = %d; want 403", resp.StatusCode)
	}
	resp, _ := r.post(t, r.member, path+"/delete", nil, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != r.base+"/plans/"+r.plan.ID.String() {
		t.Fatalf("delete = %d %s; want a redirect to the plan", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, _ := r.get(t, r.member, path); status != http.StatusNotFound {
		t.Fatalf("run page after delete = %d; want 404", status)
	}
	if got, err := repo.GetQAIssue(ctx, r.s, is.ID); err != nil || got.Title != "Submit fails" {
		t.Fatalf("issue after its run was deleted = %+v, %v", got, err)
	}
	if n := auditCount(t, r.db, r.orgID, "qa.run.delete"); n != 1 {
		t.Fatalf("qa.run.delete audit rows = %d; want 1", n)
	}
	if resp, _ := r.post(t, r.member, path+"/delete", nil, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again = %d; want 404", resp.StatusCode)
	}
}
