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

func planNamed(t *testing.T, e qaEnv, projectID uuid.UUID, name string) repo.QAPlan {
	t.Helper()
	plans, _ := repo.ListQAPlans(context.Background(), e.s, projectID)
	for _, p := range plans {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no plan %q in %+v", name, plans)
	return repo.QAPlan{}
}

func TestQAPlans_CreateListFilterAndPage(t *testing.T) {
	e := newQAEnv(t, "acme-qaplans")
	ctx := context.Background()
	member, _ := e.session(t, "qa@acme.example", "member")
	p := e.project(t, "Portal")
	seedQALibrary(t, e, p) // "Regression core" scenario, plan "Regression October" with one run
	uat, _ := repo.CreateQAScenario(ctx, e.s, p, "UAT <b>RFS</b>", "uat", "", nil)
	reg := planNamed(t, e, p, "Regression October")
	scs, _ := repo.ListQAPlanScenarioIDs(ctx, e.s, reg.ID)
	base := e.projectPath(t, p) + "/qa/plans"

	form := url.Values{"name": {"UAT September"}, "phase": {"uat"}, "environment": {"UAT (linknet-uati)"}, "build": {"1.4.2"},
		"starts_on": {"2026-09-01"}, "ends_on": {"2026-09-14"}, "scenario_id": {uat.ID.String(), scs[0].String()}}
	resp, _ := e.post(t, member, base, form, true)
	plan := planNamed(t, e, p, "UAT September")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("HX-Redirect") != base+"/"+plan.ID.String() {
		t.Fatalf("create = %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	if ids, _ := repo.ListQAPlanScenarioIDs(ctx, e.s, plan.ID); len(ids) != 2 || plan.StartsOn == nil || plan.StartsOn.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("plan = %+v, scenarios %v", plan, ids)
	}

	status, body := e.get(t, member, base)
	if status != http.StatusOK || !strings.Contains(body, "UAT September") || !strings.Contains(body, "Regression October") {
		t.Fatalf("list = %d", status)
	}
	_, body = e.get(t, member, base+"?phase=uat")
	if !strings.Contains(body, "UAT September") || strings.Contains(body, "Regression October") {
		t.Fatal("phase filter does not filter")
	}

	status, body = e.get(t, member, base+"/"+plan.ID.String())
	if status != http.StatusOK || !strings.Contains(body, "UAT &lt;b&gt;RFS&lt;/b&gt;") || strings.Contains(body, "<b>RFS</b>") ||
		!strings.Contains(body, "UAT (linknet-uati)") || !strings.Contains(body, "01 Sep – 14 Sep 2026") {
		t.Fatalf("plan page = %d:\n%s", status, body)
	}
	// The existing plan's run is listed with its cycle.
	_, body = e.get(t, member, base+"/"+reg.ID.String())
	if !strings.Contains(body, "Cycle 1") || !strings.Contains(body, `data-run-cycle="1"`) {
		t.Fatalf("runs by cycle missing:\n%s", body)
	}
}

func TestQAPlans_EditValidatesDatesAndScenarios(t *testing.T) {
	e := newQAEnv(t, "acme-qaplans-edit")
	ctx := context.Background()
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	foreign, _ := repo.CreateQAScenario(ctx, e.s, other, "Billing SIT", "sit", "", nil)
	plan, _ := repo.CreateQAPlan(ctx, e.s, p, repo.QAPlanInput{Name: "SIT", Phase: "sit"}, nil)
	path := e.projectPath(t, p) + "/qa/plans/" + plan.ID.String()

	_, body := e.post(t, e.owner, path, url.Values{"name": {"SIT"}, "phase": {"sit"}, "starts_on": {"2026-09-14"}, "ends_on": {"2026-09-01"}}, true)
	if !strings.Contains(body, "The end date is before the start date.") {
		t.Fatalf("reversed window: %s", body)
	}
	_, body = e.post(t, e.owner, path, url.Values{"name": {"SIT"}, "phase": {"sit"}, "starts_on": {"14/09/2026"}}, true)
	if !strings.Contains(body, "Dates are written as YYYY-MM-DD.") {
		t.Fatalf("bad date: %s", body)
	}
	if resp, _ := e.post(t, e.owner, path, url.Values{"name": {"SIT"}, "phase": {"sit"}, "scenario_id": {foreign.ID.String()}}, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("scenario of another project = %d; want 404", resp.StatusCode)
	}
	resp, _ := e.post(t, e.owner, path, url.Values{"name": {"SIT round 2"}, "phase": {"sit"}, "build": {"2.0"}}, true)
	got, _ := repo.GetQAPlan(ctx, e.s, plan.ID)
	if resp.StatusCode != http.StatusNoContent || got.Name != "SIT round 2" || got.Build != "2.0" {
		t.Fatalf("edit = %d, %+v", resp.StatusCode, got)
	}
}

func TestQAPlans_DuplicateCopiesScopeNotDatesAndCloseIsFinal(t *testing.T) {
	e := newQAEnv(t, "acme-qaplans-dup")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	p := e.project(t, "Portal")
	seedQALibrary(t, e, p)
	reg := planNamed(t, e, p, "Regression October")
	start := reg.CreatedAt
	repo.UpdateQAPlan(ctx, e.s, reg.ID, repo.QAPlanInput{Name: reg.Name, Phase: "regression", Environment: "Staging", Build: "1.3",
		StartsOn: &start, EndsOn: &start, ScenarioIDs: mustPlanScenarios(t, e, reg.ID)})
	base := e.projectPath(t, p) + "/qa/plans"

	resp, _ := e.post(t, e.owner, base+"/"+reg.ID.String()+"/duplicate", url.Values{"name": {"Regression November"}}, true)
	dup := planNamed(t, e, p, "Regression November")
	if resp.Header.Get("HX-Redirect") != base+"/"+dup.ID.String() || dup.Environment != "Staging" || dup.Build != "1.3" ||
		dup.StartsOn != nil || dup.EndsOn != nil || len(mustPlanScenarios(t, e, dup.ID)) != 1 {
		t.Fatalf("duplicate = %q, %+v", resp.Header.Get("HX-Redirect"), dup)
	}

	if resp, _ := e.post(t, viewer, base+"/"+dup.ID.String()+"/close", nil, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer close = %d; want 403", resp.StatusCode)
	}
	e.post(t, e.owner, base+"/"+dup.ID.String()+"/close", nil, false)
	e.post(t, e.owner, base+"/"+dup.ID.String()+"/close", nil, false)
	if n := auditCount(t, e.db, e.orgID, "qa.plan.close"); n != 1 {
		t.Fatalf("qa.plan.close audit rows = %d; want 1", n)
	}
	_, body := e.post(t, e.owner, base+"/"+dup.ID.String(), url.Values{"name": {"Reopened?"}, "phase": {"regression"}}, true)
	if !strings.Contains(body, "This plan is closed.") {
		t.Fatalf("edit of a closed plan: %s", body)
	}
	_, body = e.get(t, e.owner, base)
	if strings.Index(body, "Regression October") > strings.Index(body, "Regression November") {
		t.Fatal("open plans are not listed before closed ones")
	}
	if _, body = e.get(t, e.owner, base+"/"+dup.ID.String()); strings.Contains(body, "Edit plan…") || !strings.Contains(body, "closed") {
		t.Fatal("closed plan still offers editing")
	}
}

func mustPlanScenarios(t *testing.T, e qaEnv, planID uuid.UUID) []uuid.UUID {
	t.Helper()
	ids, err := repo.ListQAPlanScenarioIDs(context.Background(), e.s, planID)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}
