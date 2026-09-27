package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

// report posts the new-issue form the way the browser does (multipart).
func (r qaRunEnv) report(t *testing.T, raw string, form url.Values, files ...qaFile) (*http.Response, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, vs := range form {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for _, f := range files {
		fw, _ := mw.CreateFormFile("file", f.name)
		fw.Write(f.body)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://"+r.host+r.base+"/issues/new", &body)
	req.Host = r.host
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := r.app.Test(req, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func TestQAIssueNew_FromAFailedItem(t *testing.T) {
	r := newQARunEnv(t, "acme-qaissue-item")
	ctx := context.Background()
	repo.UpdateQAPlan(ctx, r.s, r.plan.ID, repo.QAPlanInput{Name: r.plan.Name, Phase: "regression", Environment: "UAT (linknet-uati)",
		ScenarioIDs: mustPlanScenarios(t, r.qaEnv, r.plan.ID)})
	run, items := r.firstRun(t)
	repo.RecordQAResult(ctx, r.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed"})

	// The item panel offers it.
	_, panel := r.get(t, r.member, r.base+"/runs/"+run.ID.String()+"/items/"+items[0].ID.String())
	if !strings.Contains(panel, r.base+"/issues/new?item="+items[0].ID.String()) {
		t.Fatal("the item panel has no Report issue link")
	}
	status, body := r.get(t, r.member, r.base+"/issues/new?item="+items[0].ID.String())
	if status != http.StatusOK || !strings.Contains(body, `value="Fail: Login"`) || !strings.Contains(body, "UAT (linknet-uati)") {
		t.Fatalf("prefilled form = %d:\n%s", status, body)
	}
	resp, _ := r.report(t, r.member, url.Values{"item_id": {items[0].ID.String()}, "title": {"Fail: Login"}, "severity": {"critical"},
		"steps_to_reproduce": {"1. Open the portal"}, "environment": {"UAT (linknet-uati)"}}, qaFile{"trace.txt", []byte("stack")})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != r.base+"/issues/2" {
		t.Fatalf("report = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	is, _ := repo.GetQAIssueByKey(ctx, r.s, r.projectID, 2)
	if is.Source != "internal" || is.PlanID == nil || *is.PlanID != r.plan.ID || is.Severity != "critical" {
		t.Fatalf("issue = %+v", is)
	}
	if linked, _ := repo.ListQAIssuesForItem(ctx, r.s, items[0].ID); len(linked) != 1 || linked[0].ID != is.ID {
		t.Fatalf("link = %+v", linked)
	}
	if files, _ := repo.ListQAAttachments(ctx, r.s, repo.QAAttachmentOwner{IssueID: &is.ID}); len(files) != 1 {
		t.Fatalf("issue files = %d; want 1", len(files))
	}
	if _, panel = r.get(t, r.member, r.base+"/runs/"+run.ID.String()+"/items/"+items[0].ID.String()); !strings.Contains(panel, "BUG-2") {
		t.Fatal("the item panel does not list its issue")
	}
}

func TestQAIssueNew_GuestReportsAsClientAndRejections(t *testing.T) {
	r := newQARunEnv(t, "acme-qaissue-guest")
	ctx := context.Background()
	viewer, _ := r.session(t, "viewer@acme.example", "viewer")

	resp, _ := r.report(t, r.guest, url.Values{"title": {"Tombol simpan tidak merespons"}, "severity": {"major"}, "assignee_id": {r.memberID.String()}})
	is, _ := repo.GetQAIssueByKey(ctx, r.s, r.projectID, 2)
	if resp.StatusCode != http.StatusFound || is == nil || is.Source != "client" || is.AssigneeID != nil {
		t.Fatalf("guest report = %d, %+v; want client source and no assignee", resp.StatusCode, is)
	}
	_, body := r.report(t, r.member, url.Values{"title": {"No severity"}})
	if !strings.Contains(body, "Choose a severity.") {
		t.Fatalf("missing severity: %s", body)
	}
	other := r.project(t, "Billing")
	otherPlan, _ := repo.CreateQAPlan(ctx, r.s, other, repo.QAPlanInput{Name: "Billing SIT", Phase: "sit"}, nil)
	if resp, _ := r.report(t, r.member, url.Values{"title": {"X"}, "severity": {"minor"}, "plan_id": {otherPlan.ID.String()}}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("plan of another project = %d; want 404", resp.StatusCode)
	}
	if resp, _ := r.report(t, viewer, url.Values{"title": {"X"}, "severity": {"minor"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer report = %d; want 403", resp.StatusCode)
	}
}
