package repo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAIssues_PagingCountAndSearch(t *testing.T) {
	f := newRunFixture(t, "acme-issue-list")
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		title := fmt.Sprintf("Crash %d", i)
		if i == 3 {
			title = "Upload at 100% quota fails"
		}
		if _, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{Title: title, Severity: "major"}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	all := repo.QAIssueFilter{}
	if n, err := repo.CountQAIssues(ctx, f.s, f.projectID, all); err != nil || n != 5 {
		t.Fatalf("count = %d, %v", n, err)
	}
	page := repo.QAIssueFilter{Limit: 2, Offset: 2}
	got, _ := repo.ListQAIssues(ctx, f.s, f.projectID, page)
	if len(got) != 2 || got[0].Key != 3 || got[1].Key != 2 {
		t.Fatalf("second page = %+v; want keys 3 and 2 (newest first)", got)
	}
	q := repo.QAIssueFilter{Query: "%"}
	if hits, _ := repo.ListQAIssues(ctx, f.s, f.projectID, q); len(hits) != 1 || hits[0].Key != 3 {
		t.Fatalf("search for a literal %% = %+v", hits)
	}
	if n, _ := repo.CountQAIssues(ctx, f.s, f.projectID, repo.QAIssueFilter{Query: "crash"}); n != 4 {
		t.Fatalf("search crash = %d; want 4", n)
	}
}
