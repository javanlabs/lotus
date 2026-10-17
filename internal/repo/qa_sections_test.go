package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func TestQASections_TreeRules(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-sec")
	p := mustProject(t, s, "portal", nil)

	auth, err := repo.CreateQASection(ctx, s, p.ID, "Auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	login, err := repo.CreateQASection(ctx, s, p.ID, "Login", &auth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateQASection(ctx, s, p.ID, "auth", nil); err != repo.ErrQANameTaken {
		t.Fatalf("duplicate top-level name: %v; want ErrQANameTaken", err)
	}
	if err := repo.MoveQASection(ctx, s, auth.ID, &login.ID); err != repo.ErrQASectionCycle {
		t.Fatalf("move into own child: %v; want ErrQASectionCycle", err)
	}
	// Build a chain to the depth limit, then one more must fail.
	parent := login
	for depth := 3; depth <= repo.MaxQASectionDepth; depth++ {
		parent, err = repo.CreateQASection(ctx, s, p.ID, "L", &parent.ID)
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	if _, err := repo.CreateQASection(ctx, s, p.ID, "L", &parent.ID); err != repo.ErrQASectionTooDeep {
		t.Fatalf("depth %d: %v; want ErrQASectionTooDeep", repo.MaxQASectionDepth+1, err)
	}
	if err := repo.ArchiveQASection(ctx, s, auth.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := repo.ListQASections(ctx, s, p.ID)
	for _, sec := range all {
		if sec.ArchivedAt == nil {
			t.Errorf("section %q not archived with its parent", sec.Name)
		}
	}
}

func TestQASections_ParentMustBeInTheSameProject(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-secx")
	p1 := mustProject(t, s, "portal", nil)
	p2 := mustProject(t, s, "mobile", nil)
	other, _ := repo.CreateQASection(ctx, s, p2.ID, "Other", nil)
	if _, err := repo.CreateQASection(ctx, s, p1.ID, "Child", &other.ID); err != repo.ErrNotFound {
		t.Fatalf("parent from another project: %v; want ErrNotFound", err)
	}
	mine, _ := repo.CreateQASection(ctx, s, p1.ID, "Mine", nil)
	if err := repo.MoveQASection(ctx, s, mine.ID, &other.ID); err != repo.ErrNotFound {
		t.Fatalf("move under another project's section: %v; want ErrNotFound", err)
	}
	missing := uuid.New()
	if _, err := repo.CreateQASection(ctx, s, p1.ID, "Orphan", &missing); err != repo.ErrNotFound {
		t.Fatalf("missing parent: %v; want ErrNotFound", err)
	}
}

func TestQASections_ArchivedSectionTakesNoNewRows(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-sec-archived")
	p := mustProject(t, s, "portal", nil)
	live, _ := repo.CreateQASection(ctx, s, p.ID, "Live", nil)
	old, _ := repo.CreateQASection(ctx, s, p.ID, "Old", nil)
	kept := mustQACase(t, s, p.ID, old.ID, "Kept")
	other := mustQACase(t, s, p.ID, live.ID, "Other")
	repo.ArchiveQASection(ctx, s, old.ID)

	if _, err := repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: old.ID, Title: "New"}, nil); err != repo.ErrQASectionArchived {
		t.Fatalf("case into archived section = %v", err)
	}
	if _, err := repo.CreateQASection(ctx, s, p.ID, "Child", &old.ID); err != repo.ErrQASectionArchived {
		t.Fatalf("section under archived parent = %v", err)
	}
	if err := repo.MoveQASection(ctx, s, live.ID, &old.ID); err != repo.ErrQASectionArchived {
		t.Fatalf("move under archived parent = %v", err)
	}
	if _, err := repo.MoveQACases(ctx, s, []uuid.UUID{other.ID}, old.ID); err != repo.ErrQASectionArchived {
		t.Fatalf("bulk move into archived section = %v", err)
	}
	if err := repo.RestoreQACase(ctx, s, kept.ID); err != repo.ErrQASectionArchived {
		t.Fatalf("restore while the section is archived = %v", err)
	}
	// Editing an archived case where it stands is fine; moving it to a live
	// section and then restoring it is the way back.
	in := repo.QACaseInput{SectionID: old.ID, Title: "Kept, edited"}
	if _, err := repo.UpdateQACase(ctx, s, kept.ID, in); err != nil {
		t.Fatalf("edit in place = %v", err)
	}
	in.SectionID = live.ID
	if _, err := repo.UpdateQACase(ctx, s, kept.ID, in); err != nil {
		t.Fatalf("move to live section = %v", err)
	}
	if err := repo.RestoreQACase(ctx, s, kept.ID); err != nil {
		t.Fatalf("restore after the move = %v", err)
	}
	in.SectionID = old.ID
	if _, err := repo.UpdateQACase(ctx, s, kept.ID, in); err != repo.ErrQASectionArchived {
		t.Fatalf("move into archived section = %v", err)
	}
}
