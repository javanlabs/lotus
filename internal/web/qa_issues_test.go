package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAIssues_ListFiltersEscapesAndPages(t *testing.T) {
	t.Parallel()
	r := newQARunEnv(t, "acme-qaissues")
	ctx := context.Background()
	mk := func(title, severity, source string) *repo.QAIssue {
		is, err := repo.CreateQAIssue(ctx, r.s, r.projectID, repo.QAIssueInput{Title: title, Severity: severity, Source: source, PlanID: &r.plan.ID}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return is
	}
	mk("Submit <b>returns</b> 500", "blocker", "internal")
	mk("Laporan klien: tombol simpan", "major", "client")
	closed := mk("Old typo", "trivial", "internal")
	repo.ChangeQAIssueStatus(ctx, r.s, closed.ID, "closed", nil, "")
	base := r.base + "/issues"

	// seedQALibrary already reported one open issue (no plan): three are open.
	status, body := r.get(t, r.guest, base)
	if status != http.StatusOK || strings.Count(body, "data-issue-row") != 3 || strings.Contains(body, "Old typo") {
		t.Fatalf("open issues = %d, %d rows:\n%s", status, strings.Count(body, "data-issue-row"), body)
	}
	if strings.Contains(body, "<b>returns</b>") || !strings.Contains(body, "Submit &lt;b&gt;returns&lt;/b&gt; 500") {
		t.Fatal("issue title not escaped")
	}
	if !strings.Contains(body, "BUG-2") || !strings.Contains(body, `data-source="client"`) || !strings.Contains(body, "Regression October") {
		t.Fatal("key, client badge or plan missing")
	}
	for q, want := range map[string]int{
		"?status=closed": 1, "?status=all": 4, "?status=all&severity=blocker": 1,
		"?status=all&source=client": 1, "?status=all&q=typo": 1, "?status=all&plan=" + r.plan.ID.String(): 3,
	} {
		if _, body = r.get(t, r.member, base+q); strings.Count(body, "data-issue-row") != want {
			t.Fatalf("%s: %d rows; want %d", q, strings.Count(body, "data-issue-row"), want)
		}
	}

	for i := 0; i < qaIssuesPerPage; i++ {
		mk(fmt.Sprintf("Bulk %03d", i), "minor", "internal")
	}
	_, body = r.get(t, r.member, base+"?page=2")
	if strings.Count(body, "data-issue-row") != 3 || !strings.Contains(body, fmt.Sprintf("%d–%d of %d issues", qaIssuesPerPage+1, qaIssuesPerPage+3, qaIssuesPerPage+3)) {
		t.Fatalf("page 2: %d rows", strings.Count(body, "data-issue-row"))
	}
	stranger, strangerID := r.session(t, "other@bank.example", "guest")
	elsewhere := r.project(t, "Elsewhere")
	repo.AddProjectMember(ctx, r.s, elsewhere, strangerID)
	if status, _ := r.get(t, stranger, base); status != http.StatusNotFound {
		t.Fatalf("guest of another project = %d; want 404", status)
	}
}
