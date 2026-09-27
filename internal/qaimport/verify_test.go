package qaimport_test

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/qaimport"
	"github.com/uyab/lotus/internal/repo"
)

func TestVerify_CleanAfterImportAndDetectsDrift(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.run(t)
	mm, err := qaimport.Verify(ctx, e.db, e.legacy, e.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mm) != 0 {
		t.Fatalf("mismatches after a clean import: %+v", mm)
	}
	// Drift: delete one imported result.
	withOrg(t, e, func(tx *sqlx.Tx) { tx.Exec(`DELETE FROM qa_results WHERE id = (SELECT id FROM qa_results LIMIT 1)`) })
	mm, _ = qaimport.Verify(ctx, e.db, e.legacy, e.orgID)
	if len(mm) != 1 || mm[0].Field != "results" || mm[0].Legacy != 2 || mm[0].Lotus != 1 {
		t.Fatalf("mismatches = %+v; want one on results", mm)
	}
}

func TestVerify_CatchesAStrayProjectWithQAData(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.run(t)
	stray, err := repo.CreateProject(ctx, e.s, repo.NewProject{Slug: "stray", Name: "Stray"})
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := repo.CreateQASection(ctx, e.s, stray.ID, "S", nil)
	repo.CreateQACase(ctx, e.s, stray.ID, repo.QACaseInput{SectionID: sec.ID, Title: "x"}, nil)
	mm, _ := qaimport.Verify(ctx, e.db, e.legacy, e.orgID)
	if len(mm) != 1 || mm[0].Field != "projects_with_qa_data" || mm[0].Legacy != 1 || mm[0].Lotus != 2 {
		t.Fatalf("mismatches = %+v; want one projects_with_qa_data", mm)
	}
}
