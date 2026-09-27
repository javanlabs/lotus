package blob_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/blob"
)

func TestFS_PutOpenDelete(t *testing.T) {
	ctx := context.Background()
	fs, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, err := fs.Put(ctx, "qa/org/2026/09/abc", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Put = %d, %v", n, err)
	}
	// Overwriting the same key replaces it (the importer relies on this).
	fs.Put(ctx, "qa/org/2026/09/abc", strings.NewReader("hello again"))
	r, err := fs.Open(ctx, "qa/org/2026/09/abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello again" {
		t.Fatalf("content = %q", b)
	}
	if err := fs.Delete(ctx, "qa/org/2026/09/abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Open(ctx, "qa/org/2026/09/abc"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Open after delete: %v; want ErrNotFound", err)
	}
}

func TestFS_RejectsKeysThatEscapeTheRoot(t *testing.T) {
	fs, _ := blob.NewFS(t.TempDir())
	for _, key := range []string{"", "/etc/passwd", "../x", "qa/../../x", "qa\\x", "qa//x"} {
		if _, err := fs.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, blob.ErrBadKey) {
			t.Errorf("Put(%q) = %v; want ErrBadKey", key, err)
		}
	}
}
