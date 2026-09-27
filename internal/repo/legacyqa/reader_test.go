package legacyqa_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo/legacyqa"
	"github.com/uyab/lotus/internal/repo/legacyqa/legacyqatest"
)

func id(n string) uuid.UUID { return uuid.MustParse("00000000-0000-0000-0000-0000000000" + n) }

func TestReader_AppliesTheInclusionRules(t *testing.T) {
	ctx := context.Background()
	r, err := legacyqa.Open(legacyqatest.LoadFixture(t, dbtest.DSN(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	projects, _ := r.Projects(ctx)
	if len(projects) != 2 || !projects[0].HasData || projects[1].HasData {
		t.Fatalf("projects = %+v; want the portal (data) and Empty (no data)", projects)
	}
	p := id("11")
	cases, _ := r.Cases(ctx, p)
	if len(cases) != 4 {
		t.Fatalf("cases = %d; want 4 (c35 deleted and unreferenced)", len(cases))
	}
	steps, _ := r.Steps(ctx, p)
	if s := steps[id("31")]; len(s) != 2 || s[0].Action != "Open https://portal" {
		t.Fatalf("steps of c31 = %+v; want ordered by position", s)
	}
	details, _ := r.Details(ctx, p)
	if len(details) != 5 {
		t.Fatalf("details = %d; want 5 (duplicate and deleted dropped)", len(details))
	}
	results, _ := r.Results(ctx, p)
	if len(results) != 3 {
		t.Fatalf("results = %d; want 3 (the dropped duplicate's result excluded)", len(results))
	}
	files, _ := r.Files(ctx, p)
	if len(files) != 3 {
		t.Fatalf("files = %d; want 3 (release attachment excluded)", len(files))
	}
	if _, err := r.DB().ExecContext(ctx, `DELETE FROM users`); err == nil {
		t.Fatal("reader connection accepted a write; it must be read-only")
	}
}
