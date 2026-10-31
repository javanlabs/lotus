package repo_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// folderTestOrg inserts an organization and returns a scope bound to it,
// the same way a request or a worker run would be bound.
func folderTestOrg(t *testing.T, db *sqlx.DB, subdomain string) (uuid.UUID, tenant.Scope) {
	t.Helper()
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	return orgID, tenant.FromRun(db, orgID)
}

func mustFolder(t *testing.T, s tenant.Scope, name string, parent *repo.Folder) *repo.Folder {
	t.Helper()
	var parentID *uuid.UUID
	if parent != nil {
		parentID = &parent.ID
	}
	f, err := repo.CreateFolder(context.Background(), s, name, parentID)
	if err != nil {
		t.Fatalf("create folder %q: %v", name, err)
	}
	return f
}

func mustProject(t *testing.T, s tenant.Scope, slug string, folder *repo.Folder) *repo.Project {
	t.Helper()
	in := repo.NewProject{Slug: slug, Name: slug}
	if folder != nil {
		in.FolderID = &folder.ID
	}
	p, err := repo.CreateProject(context.Background(), s, in)
	if err != nil {
		t.Fatalf("create project %q: %v", slug, err)
	}
	return p
}

func TestCreateFolder_TopLevelAndNested(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)

	folders, err := repo.ListFolders(context.Background(), s)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(folders) != 2 {
		t.Fatalf("listed %d folders, want 2", len(folders))
	}
	if it.ParentID != nil {
		t.Fatalf("top-level folder has parent %v", it.ParentID)
	}
	if tribe.ParentID == nil || *tribe.ParentID != it.ID {
		t.Fatalf("Tribe lead A parent = %v, want %v", tribe.ParentID, it.ID)
	}
}

func TestCreateFolder_TrimsAndRejectsBlankOrLongNames(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	f, err := repo.CreateFolder(ctx, s, "  Product  ", nil)
	if err != nil || f.Name != "Product" {
		t.Fatalf("create trimmed = %v, %v; want name %q", f, err, "Product")
	}
	if _, err := repo.CreateFolder(ctx, s, "   ", nil); !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("blank name err = %v, want ErrInvalidInput", err)
	}
	if _, err := repo.CreateFolder(ctx, s, strings.Repeat("x", repo.MaxFolderNameLength), nil); err != nil {
		t.Fatalf("80-char name err = %v, want nil", err)
	}
	if _, err := repo.CreateFolder(ctx, s, strings.Repeat("y", repo.MaxFolderNameLength+1), nil); !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("81-char name err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateFolder_SiblingNamesAreUniqueIgnoringCaseIncludingTopLevel(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)

	if _, err := repo.CreateFolder(ctx, s, "product", nil); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("duplicate top-level name err = %v, want ErrFolderNameTaken", err)
	}
	mustFolder(t, s, "Archive", product)
	if _, err := repo.CreateFolder(ctx, s, "ARCHIVE", &product.ID); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("duplicate nested name err = %v, want ErrFolderNameTaken", err)
	}
	// The same name under a different parent is fine.
	mustFolder(t, s, "Archive", nil)
}

func TestCreateFolder_DepthCapIsEightLevels(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	var parent *repo.Folder
	for i := 1; i <= repo.MaxFolderDepth; i++ {
		parent = mustFolder(t, s, "level", parent)
	}
	if _, err := repo.CreateFolder(context.Background(), s, "too deep", &parent.ID); !errors.Is(err, repo.ErrFolderTooDeep) {
		t.Fatalf("ninth level err = %v, want ErrFolderTooDeep", err)
	}
}

func TestFolders_AreIsolatedBetweenOrganizations(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, acme := folderTestOrg(t, db, "acme")
	_, other := folderTestOrg(t, db, "other")
	ctx := context.Background()
	acmeFolder := mustFolder(t, acme, "Product", nil)

	if folders, err := repo.ListFolders(ctx, other); err != nil || len(folders) != 0 {
		t.Fatalf("other org lists %d folders (err %v), want 0", len(folders), err)
	}
	if _, err := repo.CreateFolder(ctx, other, "Sneaky", &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("parent from another org err = %v, want ErrNotFound", err)
	}
	if _, err := repo.RenameFolder(ctx, other, acmeFolder.ID, "Mine now"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("rename across orgs err = %v, want ErrNotFound", err)
	}
	p := mustProject(t, other, "theirs", nil)
	if _, err := repo.MoveProject(ctx, other, p.ID, &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("filing into another org's folder err = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateProject(ctx, other, repo.NewProject{Slug: "x", Name: "x", FolderID: &acmeFolder.ID}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("creating a project in another org's folder err = %v, want ErrNotFound", err)
	}
}

func TestRenameFolder(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	mustFolder(t, s, "Product", nil)
	it := mustFolder(t, s, "IT", nil)

	renamed, err := repo.RenameFolder(ctx, s, it.ID, " IT Services ")
	if err != nil || renamed.Name != "IT Services" {
		t.Fatalf("rename = %v, %v; want IT Services", renamed, err)
	}
	if _, err := repo.RenameFolder(ctx, s, it.ID, "PRODUCT"); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("rename onto a sibling's name err = %v, want ErrFolderNameTaken", err)
	}
	if _, err := repo.RenameFolder(ctx, s, uuid.New(), "Ghost"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("rename unknown err = %v, want ErrNotFound", err)
	}
}

func TestMoveProject_IntoAFolderAndBackToTheRoot(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "alurkerja", nil)

	from, err := repo.MoveProject(ctx, s, p.ID, &product.ID)
	if err != nil || from != nil {
		t.Fatalf("first move = from %v, err %v; want from nil", from, err)
	}
	got, _ := repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID == nil || *got.FolderID != product.ID {
		t.Fatalf("FolderID = %v, want %v", got.FolderID, product.ID)
	}

	from, err = repo.MoveProject(ctx, s, p.ID, nil)
	if err != nil || from == nil || *from != product.ID {
		t.Fatalf("move to root = from %v, err %v; want from %v", from, err, product.ID)
	}
	got, _ = repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID != nil {
		t.Fatalf("FolderID = %v after moving to the root, want nil", got.FolderID)
	}
	if _, err := repo.MoveProject(ctx, s, uuid.New(), &product.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving an unknown project err = %v, want ErrNotFound", err)
	}
}

func TestCreateProject_CanBeFiledStraightIntoAFolder(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "syarq", product)
	if p.FolderID == nil || *p.FolderID != product.ID {
		t.Fatalf("FolderID = %v, want %v", p.FolderID, product.ID)
	}
}

// Review focus: a filed project that is archived keeps its folder, so
// unarchiving puts it back where it was.
func TestArchivingAProjectKeepsItsFolder(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)
	p := mustProject(t, s, "aos", product)

	if err := repo.SetProjectArchived(ctx, s, p.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := repo.SetProjectArchived(ctx, s, p.ID, false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	got, _ := repo.ProjectByID(ctx, s, p.ID)
	if got.FolderID == nil || *got.FolderID != product.ID {
		t.Fatalf("FolderID after archive round trip = %v, want %v", got.FolderID, product.ID)
	}
}

// Review focus: deleting an organization must cascade through nested
// folders and filed projects — the reason the folder keys are NO ACTION.
func TestDeletingAnOrganizationCascadesThroughNestedFolders(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	orgID, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)
	mustProject(t, s, "linknet", tribe)

	if _, err := db.Exec(`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
}

func TestMoveFolder_RefusesCycles(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)
	squad := mustFolder(t, s, "Squad 1", tribe)

	for _, target := range []*repo.Folder{it, tribe, squad} {
		if _, err := repo.MoveFolder(ctx, s, it.ID, &target.ID); !errors.Is(err, repo.ErrFolderCycle) {
			t.Fatalf("moving IT Services into %q err = %v, want ErrFolderCycle", target.Name, err)
		}
	}
}

func TestMoveFolder_ToAnotherParentAndBackToTheTop(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", nil)

	moved, err := repo.MoveFolder(ctx, s, tribe.ID, &it.ID)
	if err != nil || moved.ParentID == nil || *moved.ParentID != it.ID {
		t.Fatalf("move under IT = %v, %v", moved, err)
	}
	moved, err = repo.MoveFolder(ctx, s, tribe.ID, nil)
	if err != nil || moved.ParentID != nil {
		t.Fatalf("move to top = %v, %v", moved, err)
	}
}

func TestMoveFolder_DepthCountsTheWholeSubtree(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	// A chain six deep, and a separate folder that has one child
	// (a subtree two levels tall).
	chain := make([]*repo.Folder, 0, 7)
	var parent *repo.Folder
	for i := 1; i <= 7; i++ {
		parent = mustFolder(t, s, "level", parent)
		chain = append(chain, parent)
	}
	mover := mustFolder(t, s, "Mover", nil)
	mustFolder(t, s, "Child", mover)

	// Under level 7: 7 + 2 = 9 > 8.
	if _, err := repo.MoveFolder(ctx, s, mover.ID, &chain[6].ID); !errors.Is(err, repo.ErrFolderTooDeep) {
		t.Fatalf("move under level 7 err = %v, want ErrFolderTooDeep", err)
	}
	// Under level 6: 6 + 2 = 8, allowed.
	if _, err := repo.MoveFolder(ctx, s, mover.ID, &chain[5].ID); err != nil {
		t.Fatalf("move under level 6 err = %v, want nil", err)
	}
}

func TestMoveFolder_NameClashAtTheDestination(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	mustFolder(t, s, "Archive", it)
	archive := mustFolder(t, s, "archive", nil)

	if _, err := repo.MoveFolder(context.Background(), s, archive.ID, &it.ID); !errors.Is(err, repo.ErrFolderNameTaken) {
		t.Fatalf("err = %v, want ErrFolderNameTaken", err)
	}
}

func TestMoveFolder_AcrossOrganizationsIsNotFound(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, acme := folderTestOrg(t, db, "acme")
	_, other := folderTestOrg(t, db, "other")
	ctx := context.Background()
	acmeFolder := mustFolder(t, acme, "Product", nil)
	otherFolder := mustFolder(t, other, "Mine", nil)

	if _, err := repo.MoveFolder(ctx, other, acmeFolder.ID, nil); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving another org's folder err = %v, want ErrNotFound", err)
	}
	if _, err := repo.MoveFolder(ctx, other, otherFolder.ID, &acmeFolder.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("moving under another org's folder err = %v, want ErrNotFound", err)
	}
}

// Review focus: two people moving folders at the same moment so that each
// move is valid alone but together they form a loop. The advisory lock
// makes the second one see the first: exactly one must fail.
func TestMoveFolder_ConcurrentOppositeMovesCannotFormALoop(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		a := mustFolder(t, s, "A"+uuid.NewString()[:8], nil)
		b := mustFolder(t, s, "B"+uuid.NewString()[:8], nil)

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = repo.MoveFolder(ctx, s, a.ID, &b.ID) }()
		go func() { defer wg.Done(); <-start; _, errs[1] = repo.MoveFolder(ctx, s, b.ID, &a.ID) }()
		close(start)
		wg.Wait()

		failed := 0
		for _, err := range errs {
			if err != nil {
				if !errors.Is(err, repo.ErrFolderCycle) {
					t.Fatalf("round %d: unexpected error %v", i, err)
				}
				failed++
			}
		}
		if failed != 1 {
			t.Fatalf("round %d: %d moves failed, want exactly 1 (errs %v)", i, failed, errs)
		}
	}
}

func TestDeleteFolder_EmptyFolderIsDeleted(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	f := mustFolder(t, s, "Old", nil)

	deleted, err := repo.DeleteFolder(ctx, s, f.ID)
	if err != nil || deleted.Name != "Old" {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	if _, err := repo.FolderByID(ctx, s, f.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("lookup after delete err = %v, want ErrNotFound", err)
	}
	if _, err := repo.DeleteFolder(ctx, s, f.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteFolder_RefusesAFolderWithSubfolders(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	mustFolder(t, s, "Tribe lead A", it)

	_, err := repo.DeleteFolder(context.Background(), s, it.ID)
	var notEmpty *repo.FolderNotEmptyError
	if !errors.As(err, &notEmpty) {
		t.Fatalf("err = %v, want *FolderNotEmptyError", err)
	}
	if want := "'IT Services' still contains 1 folder."; notEmpty.Error() != want {
		t.Fatalf("message = %q, want %q", notEmpty.Error(), want)
	}
}

// Review focus: a folder whose only project is archived looks empty on the
// page but must still refuse deletion, naming the archived project.
func TestDeleteFolder_RefusesAFolderHoldingOnlyAnArchivedProject(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	tribe := mustFolder(t, s, "Tribe lead A", nil)
	p := mustProject(t, s, "Linknet", tribe)
	if err := repo.SetProjectArchived(ctx, s, p.ID, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := repo.DeleteFolder(ctx, s, tribe.ID)
	var notEmpty *repo.FolderNotEmptyError
	if !errors.As(err, &notEmpty) {
		t.Fatalf("err = %v, want *FolderNotEmptyError", err)
	}
	if want := "'Tribe lead A' still contains 1 project (Linknet — 1 archived)."; notEmpty.Error() != want {
		t.Fatalf("message = %q, want %q", notEmpty.Error(), want)
	}
}

func TestFolderNotEmptyError_CapsTheNamedProjects(t *testing.T) {
	t.Parallel()
	e := &repo.FolderNotEmptyError{
		Name: "Big", Subfolders: 2,
		Projects: []string{"a", "b", "c", "d", "e", "f", "g"},
	}
	want := "'Big' still contains 2 folders and 7 projects (a, b, c, d, e +2 more)."
	if e.Error() != want {
		t.Fatalf("message = %q, want %q", e.Error(), want)
	}
}

func TestMoveProjects_MovesEveryProjectAndReportsWhereEachCameFrom(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	product := mustFolder(t, s, "Product", nil)
	it := mustFolder(t, s, "IT Services", nil)
	a := mustProject(t, s, "a", nil)
	b := mustProject(t, s, "b", product)

	from, err := repo.MoveProjects(ctx, s, []uuid.UUID{a.ID, b.ID, a.ID}, &it.ID)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(from) != 2 || from[a.ID] != nil || from[b.ID] == nil || *from[b.ID] != product.ID {
		t.Fatalf("from = %v, want a at the root and b from Product", from)
	}
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		got, _ := repo.ProjectByID(ctx, s, id)
		if got.FolderID == nil || *got.FolderID != it.ID {
			t.Fatalf("project %s FolderID = %v, want %v", id, got.FolderID, it.ID)
		}
	}
}

func TestMoveProjects_IsAllOrNothing(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	_, other := folderTestOrg(t, db, "other")
	ctx := context.Background()
	it := mustFolder(t, s, "IT Services", nil)
	a := mustProject(t, s, "a", nil)
	theirs := mustProject(t, other, "theirs", nil)

	for name, ids := range map[string][]uuid.UUID{
		"unknown id":        {a.ID, uuid.New()},
		"another org's one": {a.ID, theirs.ID},
	} {
		if _, err := repo.MoveProjects(ctx, s, ids, &it.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", name, err)
		}
		got, _ := repo.ProjectByID(ctx, s, a.ID)
		if got.FolderID != nil {
			t.Fatalf("%s: a was moved although the batch failed", name)
		}
	}
	if _, err := repo.MoveProjects(ctx, s, nil, &it.ID); !errors.Is(err, repo.ErrInvalidInput) {
		t.Fatalf("empty batch err = %v, want ErrInvalidInput", err)
	}
}

// A deleted folder keeps its row, frees its name, and a deleted project inside
// does not keep a folder from being deleted.
func TestDeleteFolder_IsSoftAndIgnoresDeletedProjects(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme")
	ctx := context.Background()
	tribe := mustFolder(t, s, "Tribe", nil)
	p := mustProject(t, s, "Linknet", tribe)
	if _, err := repo.DeleteFolder(ctx, s, tribe.ID); err == nil {
		t.Fatal("deleted a folder holding a live project")
	}
	if err := repo.SoftDeleteProject(ctx, s, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteFolder(ctx, s, tribe.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.FolderByID(ctx, s, tribe.ID); err == nil {
		t.Error("a deleted folder is still found")
	}
	mustFolder(t, s, "Tribe", nil)
}
