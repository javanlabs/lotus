package repo_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/repo"
)

func TestQAItems_CommentLimitRefusesBeforeWriting(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-comment-limit")
	ctx := context.Background()
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})

	// The limit counts characters, not bytes: 2,500 two-byte runes fit.
	full := strings.Repeat("é", repo.MaxQACommentLength)
	if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", Comment: full}); err != nil {
		t.Fatalf("comment at the limit: %v", err)
	}
	_, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[1].ID, Status: "failed", Comment: full + "x"})
	if !errors.Is(err, repo.ErrQACommentTooLong) || !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("comment over the limit: %v; want ErrQACommentTooLong, an ErrInvalidInput", err)
	}
	got, _ := repo.GetQARunItem(ctx, f.s, items[1].ID)
	hist, _ := repo.ListQAResults(ctx, f.s, items[1].ID)
	if got.Status != "untested" || len(hist) != 0 {
		t.Fatalf("a refused result changed the item: %s, %d results", got.Status, len(hist))
	}
	// Surrounding blanks are trimmed before counting, as before storing.
	if _, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[1].ID, Status: "passed", Comment: "  " + full + "\n"}); err != nil {
		t.Fatalf("padded comment at the limit: %v", err)
	}
}

func TestQAItems_LatestResultPerItem(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-latest-result")
	ctx := context.Background()
	tester := mustUser(t, f.db, "tester@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})

	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", Comment: "500 on submit", CreatedBy: &tester})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "passed", CreatedBy: &tester})
	repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[1].ID, Status: "blocked", Comment: "No test data", CreatedBy: &tester})

	ids := []uuid.UUID{items[0].ID, items[1].ID, items[2].ID}
	latest, err := repo.LatestQAResults(ctx, f.s, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("latest = %+v; want one for each item with results", latest)
	}
	if r := latest[items[0].ID]; r.Status != "passed" || r.Comment != "" {
		t.Fatalf("item 1 latest = %+v; want the later, blank pass", r)
	}
	if r := latest[items[1].ID]; r.Comment != "No test data" || r.CreatedBy == nil || *r.CreatedBy != tester {
		t.Fatalf("item 2 latest = %+v", r)
	}
	if empty, err := repo.LatestQAResults(ctx, f.s, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no items = %v, %v", empty, err)
	}
}

func TestQARuns_NotesLimitOnCreateAndUpdate(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-notes")
	ctx := context.Background()
	long := strings.Repeat("ñ", repo.MaxQARunNotesLength+1)

	_, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1", Notes: long}, nil)
	if !errors.Is(err, repo.ErrQANotesTooLong) || !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("create with long notes: %v; want ErrQANotesTooLong", err)
	}
	if runs, _ := repo.ListQARuns(ctx, f.s, f.plan.ID); len(runs) != 0 {
		t.Fatalf("a refused run was created: %+v", runs)
	}
	run, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1", Notes: long[:len(long)-2]}, nil)
	if err != nil {
		t.Fatalf("notes at the limit: %v", err)
	}
	if _, _, err := repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: "C1", Notes: long}); !errors.Is(err, repo.ErrQANotesTooLong) {
		t.Fatalf("update with long notes: %v; want ErrQANotesTooLong", err)
	}
	if _, _, err := repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: " ", Notes: ""}); !errors.Is(err, repo.ErrInvalidInput) || errors.Is(err, repo.ErrQANotesTooLong) {
		t.Fatalf("update without a name: %v; want ErrInvalidInput", err)
	}
}

func TestQARuns_AssigneeFillsUnassignedItemsOnCreate(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-lead")
	ctx := context.Background()
	lead := mustUser(t, f.db, "lead@acme.example")
	b := mustUser(t, f.db, "b@acme.example")

	run, err := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1", Assignee: &lead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.AssigneeID == nil || *run.AssigneeID != lead {
		t.Fatalf("run assignee = %v; want the lead", run.AssigneeID)
	}
	_, per, _ := repo.QARunProgress(ctx, f.s, run.ID)
	if per[lead].Total != 3 {
		t.Fatalf("the lead holds %d items; want all 3 (none were distributed)", per[lead].Total)
	}
	// Distributed items keep their tester; the run's assignee is the
	// default only for what distribution left unassigned.
	rr, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C2", Assignee: &lead, Testers: []uuid.UUID{b}, Distribution: repo.DistributeRoundRobin}, nil)
	if _, per, _ = repo.QARunProgress(ctx, f.s, rr.ID); per[b].Total != 3 || per[lead].Total != 0 {
		t.Fatalf("round robin with a lead = %+v", per)
	}
	p, _ := repo.PreviewQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Assignee: &lead})
	if p.PerTester[lead] != 3 || p.Unassigned != 0 {
		t.Fatalf("preview = %+v; want what create does", p)
	}
}

func TestQARuns_UpdateSyncsOnlyItemsThatFollowedTheRun(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-sync")
	ctx := context.Background()
	a := mustUser(t, f.db, "a@acme.example")
	b := mustUser(t, f.db, "b@acme.example")
	c := mustUser(t, f.db, "c@acme.example")
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1", Notes: "old", Assignee: &a}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	repo.AssignQARunItems(ctx, f.s, run.ID, []uuid.UUID{items[2].ID}, &b) // a per-item override

	// Name and notes alone touch no item.
	got, changed, err := repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: " Cycle one ", Notes: "new", Assignee: &a})
	if err != nil || changed || got.Name != "Cycle one" || got.Notes != "new" {
		t.Fatalf("rename = %+v, changed %v, %v", got, changed, err)
	}
	if _, per, _ := repo.QARunProgress(ctx, f.s, run.ID); per[a].Total != 2 || per[b].Total != 1 {
		t.Fatalf("a rename moved items: %+v", per)
	}

	got, changed, err = repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: "Cycle one", Notes: "new", Assignee: &c})
	if err != nil || !changed || got.AssigneeID == nil || *got.AssigneeID != c {
		t.Fatalf("reassign = %+v, changed %v, %v", got, changed, err)
	}
	after, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	if *after[0].AssigneeID != c || *after[1].AssigneeID != c || *after[2].AssigneeID != b {
		t.Fatalf("after reassign: %v %v %v; want c c b", *after[0].AssigneeID, *after[1].AssigneeID, *after[2].AssigneeID)
	}

	// Clearing the run's assignee clears the items that followed it; from
	// nobody, the unassigned items follow the next one.
	repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: "Cycle one", Assignee: nil})
	if _, per, _ := repo.QARunProgress(ctx, f.s, run.ID); per[uuid.Nil].Total != 2 || per[b].Total != 1 {
		t.Fatalf("after clearing: %+v", per)
	}
	repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: "Cycle one", Assignee: &a})
	if _, per, _ := repo.QARunProgress(ctx, f.s, run.ID); per[a].Total != 2 || per[b].Total != 1 {
		t.Fatalf("from nobody to a: %+v", per)
	}

	repo.CloseQARun(ctx, f.s, run.ID)
	if _, _, err := repo.UpdateQARun(ctx, f.s, run.ID, repo.QARunUpdate{Name: "x"}); err != repo.ErrQAClosed {
		t.Fatalf("update a closed run: %v; want ErrQAClosed", err)
	}
	if _, _, err := repo.UpdateQARun(ctx, f.s, uuid.New(), repo.QARunUpdate{Name: "x"}); err != repo.ErrNotFound {
		t.Fatalf("update a missing run: %v; want ErrNotFound", err)
	}
}

// runWithIssue is a run whose first item failed with a file attached and an
// issue reported from it.
func runWithIssue(t *testing.T, f runFixture, store blob.Store) (*repo.QARun, []repo.QARunItem, *repo.QAIssue, *repo.QAAttachment) {
	t.Helper()
	ctx := context.Background()
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	res, err := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed", Comment: "500"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.SaveQAAttachment(ctx, f.s, store, repo.QAAttachmentOwner{ResultID: &res.ID}, "shot.png", "", bytes.NewReader([]byte("\x89PNG\r\n\x1a\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	is, err := repo.CreateQAIssue(ctx, f.s, f.projectID, repo.QAIssueInput{Title: "Submit fails", Severity: "major", Source: "internal"}, nil, &items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, items, is, a
}

func TestQARuns_RemoveItemsKeepsTheirIssues(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-remove")
	ctx := context.Background()
	store, _ := blob.NewFS(t.TempDir())
	run, items, is, a := runWithIssue(t, f, store)
	other, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C2"}, nil)
	otherItems, _ := repo.ListQARunItems(ctx, f.s, other.ID, repo.QAItemFilter{})

	n, err := repo.RemoveQARunItems(ctx, f.s, store, run.ID, []uuid.UUID{items[0].ID, items[1].ID, otherItems[0].ID})
	if err != nil || n != 2 {
		t.Fatalf("remove = %d, %v; want 2 (the other run's item is skipped)", n, err)
	}
	if left, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{}); len(left) != 1 || left[0].ID != items[2].ID {
		t.Fatalf("items left = %+v", left)
	}
	if left, _ := repo.ListQARunItems(ctx, f.s, other.ID, repo.QAItemFilter{}); len(left) != 3 {
		t.Fatal("an item of another run was removed")
	}
	if got, err := repo.GetQAIssue(ctx, f.s, is.ID); err != nil || got.Title != "Submit fails" {
		t.Fatalf("issue after removing its item = %+v, %v; want it kept", got, err)
	}
	if linked, _ := repo.ListQAItemsForIssue(ctx, f.s, is.ID); len(linked) != 0 {
		t.Fatalf("issue still links %d items", len(linked))
	}
	if _, err := store.Open(ctx, a.StorageKey); err != blob.ErrNotFound {
		t.Fatalf("the removed result's file: %v; want it deleted", err)
	}

	repo.CloseQARun(ctx, f.s, run.ID)
	if _, err := repo.RemoveQARunItems(ctx, f.s, store, run.ID, []uuid.UUID{items[2].ID}); err != repo.ErrQAClosed {
		t.Fatalf("remove from a closed run: %v; want ErrQAClosed", err)
	}
}

func TestQARuns_DeleteKeepsIssuesAndRetests(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-run-delete")
	ctx := context.Background()
	store, _ := blob.NewFS(t.TempDir())
	run, _, is, a := runWithIssue(t, f, store)
	retest, err := repo.CreateQARetest(ctx, f.s, run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo.CloseQARun(ctx, f.s, run.ID) // a closed run can still be deleted

	if err := repo.DeleteQARun(ctx, f.s, store, run.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetQARun(ctx, f.s, run.ID); err != repo.ErrNotFound {
		t.Fatalf("run after delete: %v; want ErrNotFound", err)
	}
	if got, err := repo.GetQAIssue(ctx, f.s, is.ID); err != nil || got.Title != "Submit fails" {
		t.Fatalf("issue after deleting its run = %+v, %v; want it kept", got, err)
	}
	if got, err := repo.GetQARun(ctx, f.s, retest.ID); err != nil || got.RetestOf != nil {
		t.Fatalf("retest after deleting its origin = %+v, %v; want it kept, retest_of cleared", got, err)
	}
	if _, err := store.Open(ctx, a.StorageKey); err != blob.ErrNotFound {
		t.Fatalf("the deleted run's file: %v; want it deleted", err)
	}
	if err := repo.DeleteQARun(ctx, f.s, store, run.ID); err != repo.ErrNotFound {
		t.Fatalf("delete twice: %v; want ErrNotFound", err)
	}
	// Without a file store the rows still go; the files are left behind.
	if err := repo.DeleteQARun(ctx, f.s, nil, retest.ID); err != nil {
		t.Fatalf("delete without a store: %v", err)
	}
}
