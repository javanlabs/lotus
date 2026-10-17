package repo_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/repo"
)

func TestQAAttachments_SaveListDownloadDelete(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, "acme-att")
	ctx := context.Background()
	store, _ := blob.NewFS(t.TempDir())
	run, _ := repo.CreateQARun(ctx, f.s, f.plan.ID, repo.QARunInput{Name: "C1"}, nil)
	items, _ := repo.ListQARunItems(ctx, f.s, run.ID, repo.QAItemFilter{})
	res, _ := repo.RecordQAResult(ctx, f.s, repo.QAResultInput{ItemID: items[0].ID, Status: "failed"})

	owner := repo.QAAttachmentOwner{ResultID: &res.ID}
	a, err := repo.SaveQAAttachment(ctx, f.s, store, owner, "../../evil/screen shot.png", "", bytes.NewReader([]byte("\x89PNG\r\n\x1a\nxxxx")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.FileName != "screen shot.png" || a.ContentType != "image/png" || a.SizeBytes != 12 {
		t.Fatalf("attachment = %+v", a)
	}
	list, _ := repo.ListQAAttachments(ctx, f.s, owner)
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	pid, err := repo.QAAttachmentProjectID(ctx, f.s, a.ID)
	if err != nil || pid != f.projectID {
		t.Fatalf("project = %v, %v", pid, err)
	}
	r, _ := store.Open(ctx, a.StorageKey)
	b, _ := io.ReadAll(r)
	r.Close()
	if len(b) != 12 {
		t.Fatalf("stored %d bytes", len(b))
	}
	if err := repo.DeleteQAAttachment(ctx, f.s, store, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, a.StorageKey); err != blob.ErrNotFound {
		t.Fatalf("blob after delete: %v", err)
	}

	big := strings.NewReader(strings.Repeat("x", repo.MaxQAAttachmentBytes+1))
	if _, err := repo.SaveQAAttachment(ctx, f.s, store, owner, "big.bin", "", big, nil); err != repo.ErrQAAttachmentTooLarge {
		t.Fatalf("oversize: %v; want ErrQAAttachmentTooLarge", err)
	}
	if _, err := repo.SaveQAAttachment(ctx, f.s, store, repo.QAAttachmentOwner{}, "x", "", strings.NewReader("x"), nil); err != repo.ErrInvalidInput {
		t.Fatalf("no owner: %v; want ErrInvalidInput", err)
	}
}
