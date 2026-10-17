package repo_test

import (
	"context"
	"errors"
	"testing"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func TestImportQACases_CreatesSectionsByPathInOneTransaction(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-case-import")
	p := mustProject(t, s, "portal", nil)
	auth, _ := repo.CreateQASection(ctx, s, p.ID, "Auth", nil)
	mustQACase(t, s, p.ID, auth.ID, "Existing")

	rows := []repo.QACaseImport{
		{SectionPath: []string{"auth", "Login"}, Case: repo.QACaseInput{Title: "Login valid", Steps: repo.QASteps{{Action: "Open", Expected: "Page"}}}},
		{SectionPath: []string{"Auth", "login"}, Case: repo.QACaseInput{Title: "Login invalid"}},
		{SectionPath: []string{"Billing"}, Case: repo.QACaseInput{Title: "Pay"}},
	}
	cases, sections, err := repo.ImportQACases(ctx, s, p.ID, rows, nil)
	if err != nil || cases != 3 || sections != 2 {
		t.Fatalf("import = %d cases, %d sections, %v; want 3, 2 (Login and Billing; Auth matched ignoring case)", cases, sections, err)
	}
	c2, _ := repo.GetQACaseByKey(ctx, s, p.ID, 2)
	c3, _ := repo.GetQACaseByKey(ctx, s, p.ID, 3)
	if c2.Title != "Login valid" || c3.Title != "Login invalid" || c2.SectionID != c3.SectionID || len(c2.Steps) != 1 {
		t.Fatalf("keys continue after the existing case and share Login: %+v %+v", c2, c3)
	}

	// A path through an archived section fails the whole import: nothing lands.
	old, _ := repo.CreateQASection(ctx, s, p.ID, "Old", nil)
	repo.ArchiveQASection(ctx, s, old.ID)
	_, _, err = repo.ImportQACases(ctx, s, p.ID, []repo.QACaseImport{
		{SectionPath: []string{"Fresh"}, Case: repo.QACaseInput{Title: "A"}},
		{SectionPath: []string{"Old", "Deep"}, Case: repo.QACaseInput{Title: "B"}},
	}, nil)
	var archived *repo.QASectionArchivedError
	if !errors.As(err, &archived) || archived.Path != "Old" {
		t.Fatalf("archived path = %v; want QASectionArchivedError{Old}", err)
	}
	if n, _ := repo.CountQACases(ctx, s, p.ID, repo.QACaseFilter{IncludeArchived: true}); n != 4 {
		t.Fatalf("cases after a failed import = %d; want 4 (nothing from it)", n)
	}
	secs, _ := repo.ListQASections(ctx, s, p.ID)
	for _, sec := range secs {
		if sec.Name == "Fresh" {
			t.Fatal("a failed import left a section behind")
		}
	}
	if _, _, err := repo.ImportQACases(ctx, s, p.ID, []repo.QACaseImport{{SectionPath: []string{"Auth"}, Case: repo.QACaseInput{Title: " "}}}, nil); err != repo.ErrInvalidInput {
		t.Fatalf("blank title = %v; want ErrInvalidInput", err)
	}
}
