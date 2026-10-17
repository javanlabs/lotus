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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
