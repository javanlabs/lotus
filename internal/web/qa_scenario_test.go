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

func scenarioOrder(t *testing.T, e qaEnv, scenarioID uuid.UUID) string {
	t.Helper()
	entries, _ := repo.ListQAScenarioEntries(context.Background(), e.s, scenarioID)
	var out []string
	for _, en := range entries {
		out = append(out, en.GroupName+":"+en.Title)
	}
	return strings.Join(out, ", ")
}

func TestQAScenario_BuildUATWithTwoGroups(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qascenario")
	ctx := context.Background()
	member, _ := e.session(t, "qa@acme.example", "member")
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "RFS", nil)
	var ids []string
	for _, title := range []string{"Create RFS", "Approve RFS", "Reject RFS", "Report"} {
		c, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: title}, nil)
		ids = append(ids, c.ID.String())
	}
	sc, _ := repo.CreateQAScenario(ctx, e.s, p, "UAT alur RFS", "uat", "", nil)
	base := e.projectPath(t, p) + "/qa/scenarios/" + sc.ID.String()

	for _, name := range []string{"Day 1", "Day 2"} {
		if resp, _ := e.post(t, member, base+"/groups", url.Values{"name": {name}}, true); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("add group %s = %d", name, resp.StatusCode)
		}
	}
	groups, _ := repo.ListQAScenarioGroups(ctx, e.s, sc.ID)
	day1, day2 := groups[0].ID.String(), groups[1].ID.String()

	// The picker lists matching cases not yet in the scenario.
	status, body := e.get(t, member, base+"/picker?q=rfs")
	if status != http.StatusOK || !strings.Contains(body, "Approve RFS") || strings.Contains(body, "Report") {
		t.Fatalf("picker = %d:\n%s", status, body)
	}
	e.post(t, member, base+"/cases", url.Values{"case_id": {ids[0], ids[1]}, "group_id": {day1}}, true)
	e.post(t, member, base+"/cases", url.Values{"case_id": {ids[2], ids[0]}, "group_id": {day2}}, true) // ids[0] already in: no-op
	if got := scenarioOrder(t, e, sc.ID); got != "Day 1:Create RFS, Day 1:Approve RFS, Day 2:Reject RFS" {
		t.Fatalf("after adding: %s", got)
	}
	if _, body = e.get(t, member, base+"/picker?q=rfs"); strings.Contains(body, "Approve RFS") {
		t.Fatal("picker offers a case already in the scenario")
	}

	entries, _ := repo.ListQAScenarioEntries(ctx, e.s, sc.ID)
	approve := entries[1].ItemID.String()
	e.post(t, member, base+"/items/"+approve+"/move", url.Values{"dir": {"up"}}, true)
	e.post(t, member, base+"/groups/"+day2+"/move", url.Values{"dir": {"up"}}, true)
	if got := scenarioOrder(t, e, sc.ID); got != "Day 2:Reject RFS, Day 1:Approve RFS, Day 1:Create RFS" {
		t.Fatalf("after reordering: %s", got)
	}
	e.post(t, member, base+"/items/group", url.Values{"item_id": {approve}, "group_id": {day2}}, true)
	e.post(t, member, base+"/groups/"+day1+"/rename", url.Values{"name": {"Hari 1"}}, true)
	e.post(t, member, base+"/cases/remove", url.Values{"case_id": {ids[2]}}, true)
	if got := scenarioOrder(t, e, sc.ID); got != "Day 2:Approve RFS, Hari 1:Create RFS" {
		t.Fatalf("after regroup, rename, remove: %s", got)
	}

	status, body = e.get(t, member, base)
	if status != http.StatusOK || !strings.Contains(body, "Hari 1") || !strings.Contains(body, "Add cases…") {
		t.Fatalf("scenario page = %d", status)
	}
}

func TestQAScenario_RejectsForeignIDsArchivedAndReaders(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-qascenario-bad")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	otherSec, _ := repo.CreateQASection(ctx, e.s, other, "Invoices", nil)
	foreignCase, _ := repo.CreateQACase(ctx, e.s, other, repo.QACaseInput{SectionID: otherSec.ID, Title: "Pay"}, nil)
	otherScenario, _ := repo.CreateQAScenario(ctx, e.s, other, "Billing SIT", "sit", "", nil)
	foreignGroup, _ := repo.CreateQAScenarioGroup(ctx, e.s, otherScenario.ID, "Elsewhere")
	sc, _ := repo.CreateQAScenario(ctx, e.s, p, "UAT", "uat", "", nil)
	base := e.projectPath(t, p) + "/qa/scenarios/" + sc.ID.String()

	resp, body := e.post(t, e.owner, base+"/cases", url.Values{"case_id": {foreignCase.ID.String()}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "not in this project") {
		t.Fatalf("add another project's case = %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.post(t, e.owner, base+"/groups/"+foreignGroup.ID.String()+"/rename", url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("rename another scenario's group = %d; want 404", resp.StatusCode)
	}
	if resp, _ := e.post(t, viewer, base+"/groups", url.Values{"name": {"X"}}, false); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer add group = %d; want 403", resp.StatusCode)
	}

	repo.ArchiveQAScenario(ctx, e.s, sc.ID)
	resp, body = e.post(t, e.owner, base+"/groups", url.Values{"name": {"Late"}}, true)
	if !strings.Contains(body, "This scenario is archived.") {
		t.Fatalf("write to an archived scenario = %d %s", resp.StatusCode, body)
	}
}
