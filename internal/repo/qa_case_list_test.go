package repo_test

import (
	"context"
	"testing"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func TestQACases_SectionFilterSubsectionsAndPaging(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-case-list")
	p := mustProject(t, s, "portal", nil)
	auth, _ := repo.CreateQASection(ctx, s, p.ID, "Auth", nil)
	login, _ := repo.CreateQASection(ctx, s, p.ID, "Login", &auth.ID)
	mustQACase(t, s, p.ID, auth.ID, "Session expires")
	mustQACase(t, s, p.ID, login.ID, "Login valid")
	mustQACase(t, s, p.ID, login.ID, "Login invalid")
	old := mustQACase(t, s, p.ID, login.ID, "Login with LDAP")
	repo.ArchiveQACase(ctx, s, old.ID)

	only, _ := repo.ListQACases(ctx, s, p.ID, repo.QACaseFilter{SectionID: &auth.ID})
	if len(only) != 1 {
		t.Fatalf("Auth without subsections = %d; want 1", len(only))
	}
	f := repo.QACaseFilter{SectionID: &auth.ID, IncludeSubsections: true}
	tree, _ := repo.ListQACases(ctx, s, p.ID, f)
	if len(tree) != 3 {
		t.Fatalf("Auth with subsections = %d; want 3", len(tree))
	}
	if n, err := repo.CountQACases(ctx, s, p.ID, f); err != nil || n != 3 {
		t.Fatalf("CountQACases = %d, %v; want 3", n, err)
	}
	f.IncludeArchived = true
	if n, _ := repo.CountQACases(ctx, s, p.ID, f); n != 4 {
		t.Fatalf("count with archived = %d; want 4", n)
	}

	f = repo.QACaseFilter{Limit: 2, Offset: 2}
	page, _ := repo.ListQACases(ctx, s, p.ID, f)
	if len(page) != 1 || page[0].Title != "Login invalid" {
		t.Fatalf("second page = %+v; want the third live case by key", page)
	}

	mustQACase(t, s, p.ID, auth.ID, "Upload at 100% quota")
	if hits, _ := repo.ListQACases(ctx, s, p.ID, repo.QACaseFilter{Query: "%"}); len(hits) != 1 {
		t.Fatalf("search for %%: %d hits; want only the case with a literal %%", len(hits))
	}

	counts, err := repo.QASectionCaseCounts(ctx, s, p.ID)
	if err != nil || counts[auth.ID] != 2 || counts[login.ID] != 2 {
		t.Fatalf("section counts = %v, %v; want Auth 2, Login 2 (live cases, own section only)", counts, err)
	}
}
