package repo_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func tagTestOrg(t *testing.T, db *sqlx.DB, subdomain string) tenant.Scope {
	t.Helper()
	var orgID uuid.UUID
	if err := db.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ($1, $1) RETURNING id`, subdomain); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	return tenant.FromRun(db, orgID)
}

func mustTag(t *testing.T, s tenant.Scope, projectID uuid.UUID, name string) repo.Tag {
	t.Helper()
	tag, err := repo.AddProjectTag(context.Background(), s, projectID, name)
	if err != nil {
		t.Fatalf("add tag %q: %v", name, err)
	}
	return tag
}

func tagNames(t *testing.T, s tenant.Scope) []string {
	t.Helper()
	tags, err := repo.ListTags(context.Background(), s)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	names := make([]string, len(tags))
	for i, tag := range tags {
		names[i] = tag.Name
	}
	return names
}

func TestAddProjectTag_ReusesTheTagIgnoringCaseAndSpaces(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p1 := mustProject(t, s, "pay", nil)
	p2 := mustProject(t, s, "hr", nil)

	first := mustTag(t, s, p1.ID, "PROJECT")
	second := mustTag(t, s, p2.ID, " project ")
	if second.ID != first.ID || second.Name != "PROJECT" {
		t.Fatalf("second add = %+v, want the existing %+v", second, first)
	}
	// Adding a tag the project already carries is a no-op returning it.
	again := mustTag(t, s, p1.ID, "Project")
	if again.ID != first.ID {
		t.Fatalf("re-adding = %+v, want %+v", again, first)
	}
	if got := tagNames(t, s); !slices.Equal(got, []string{"PROJECT"}) {
		t.Fatalf("tags = %v, want [PROJECT]", got)
	}
	byProject, err := repo.ProjectTagsByProject(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(byProject[p1.ID]) != 1 || len(byProject[p2.ID]) != 1 {
		t.Fatalf("tags by project = %v, want one each", byProject)
	}
}

func TestAddProjectTag_RejectsEmptyAndTooLong(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p := mustProject(t, s, "pay", nil)
	for _, name := range []string{"  ", strings.Repeat("x", 41)} {
		if _, err := repo.AddProjectTag(context.Background(), s, p.ID, name); !errors.Is(err, repo.ErrInvalidTag) {
			t.Errorf("AddProjectTag(%q) err = %v, want ErrInvalidTag", name, err)
		}
	}
	// 40 characters, counted as runes, is the limit and is allowed.
	if _, err := repo.AddProjectTag(context.Background(), s, p.ID, strings.Repeat("é", 40)); err != nil {
		t.Errorf("40-rune name: %v", err)
	}
}

func TestRemoveProjectTag_DropsTheTagWithItsLastProject(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	p1 := mustProject(t, s, "pay", nil)
	p2 := mustProject(t, s, "hr", nil)
	tag := mustTag(t, s, p1.ID, "PROJECT")
	mustTag(t, s, p2.ID, "PROJECT")

	if err := repo.RemoveProjectTag(context.Background(), s, p1.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if got := tagNames(t, s); !slices.Equal(got, []string{"PROJECT"}) {
		t.Fatalf("after removing from p1, tags = %v, want [PROJECT]", got)
	}
	if err := repo.RemoveProjectTag(context.Background(), s, p2.ID, tag.ID); err != nil {
		t.Fatal(err)
	}
	if got := tagNames(t, s); len(got) != 0 {
		t.Fatalf("after removing from its last project, tags = %v, want none", got)
	}
	var left int
	if err := s.Tx(context.Background(), func(tx *sqlx.Tx) error {
		return tx.Get(&left, `SELECT count(*) FROM tags`)
	}); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d tag rows left, want the orphan deleted", left)
	}
}

func TestProjectIDsWithAnyTag_IsAnyOf(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	s := tagTestOrg(t, db, "acme")
	ctx := context.Background()
	p1 := mustProject(t, s, "pay", nil)
	p2 := mustProject(t, s, "hr", nil)
	mustProject(t, s, "misc", nil)
	gone := mustProject(t, s, "gone", nil)
	project := mustTag(t, s, p1.ID, "PROJECT")
	product := mustTag(t, s, p2.ID, "PRODUCT")
	mustTag(t, s, gone.ID, "PROJECT")
	if err := repo.SoftDeleteProject(ctx, s, gone.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(tagIDs ...uuid.UUID) []uuid.UUID {
		t.Helper()
		got, err := repo.ProjectIDsWithAnyTag(ctx, s, tagIDs)
		if err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(got, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		return got
	}
	want := []uuid.UUID{p1.ID, p2.ID}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if got := ids(project.ID, product.ID); !slices.Equal(got, want) {
		t.Errorf("{PROJECT, PRODUCT} = %v, want %v", got, want)
	}
	if got := ids(project.ID); !slices.Equal(got, []uuid.UUID{p1.ID}) {
		t.Errorf("{PROJECT} = %v, want [%v] (the deleted project never counts)", got, p1.ID)
	}
	if got := ids(); len(got) != 0 {
		t.Errorf("{} = %v, want none", got)
	}
	byProject, err := repo.ProjectTagsByProject(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byProject[gone.ID]; ok {
		t.Errorf("tags by project includes the deleted project")
	}
}

func TestTags_AreTenantScoped(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	a := tagTestOrg(t, db, "org-a")
	b := tagTestOrg(t, db, "org-b")
	pa := mustProject(t, a, "pay", nil)
	pb := mustProject(t, b, "pay", nil)
	mustTag(t, b, pb.ID, "XLSMART")

	if got := tagNames(t, a); len(got) != 0 {
		t.Fatalf("org A sees tags %v, want none", got)
	}
	// Org A naming org B's project resolves to nothing.
	if _, err := repo.AddProjectTag(context.Background(), a, pb.ID, "PROJECT"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("tagging another org's project err = %v, want ErrNotFound", err)
	}
	// The same name in org A is a separate tag.
	tag := mustTag(t, a, pa.ID, "xlsmart")
	if tag.Name != "xlsmart" {
		t.Fatalf("org A tag = %+v, want its own spelling", tag)
	}
}
