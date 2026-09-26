package repo_test

import (
	"context"
	"errors"
	"strings"
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
	db := dbtest.Open(t)
	orgID, s := folderTestOrg(t, db, "acme")
	it := mustFolder(t, s, "IT Services", nil)
	tribe := mustFolder(t, s, "Tribe lead A", it)
	mustProject(t, s, "linknet", tribe)

	if _, err := db.Exec(`DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
}
