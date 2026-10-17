package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func TestQAImportWriter_KeepsTimestampsAndRemembersIDs(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-imp")
	legacySection := uuid.New()
	old := time.Date(2025, 2, 1, 10, 0, 0, 0, time.UTC)

	err := repo.WithQAImportTx(ctx, s, func(w *repo.QAImportWriter) error {
		pid, err := w.EnsureProject("portal", "Portal", nil, false)
		if err != nil {
			return err
		}
		again, _ := w.EnsureProject("portal", "Portal", nil, true)
		if again != pid {
			t.Errorf("EnsureProject(reuse) created a second project")
		}
		twin, err := w.EnsureProject("portal", "Portal (other legacy project)", nil, false)
		if err != nil || twin == pid {
			t.Errorf("EnsureProject(no reuse) = %v, %v; want a new project", twin, err)
		}
		sid, err := w.InsertSection(repo.QASection{ProjectID: pid, Name: "Auth", CreatedAt: old})
		if err != nil {
			return err
		}
		if err := w.Remember("modules", legacySection, sid); err != nil {
			return err
		}
		got, ok, err := w.Lookup("modules", legacySection)
		if err != nil || !ok || got != sid {
			t.Errorf("Lookup = %v, %v, %v", got, ok, err)
		}
		_, err = w.InsertCase(repo.QACase{ProjectID: pid, SectionID: sid, Key: 7, Title: "Login",
			Steps: repo.QASteps{}, Version: 1, CreatedAt: old, UpdatedAt: old})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := repo.ListQALegacyIDs(ctx, s, "modules")
	if len(ids) != 1 {
		t.Fatalf("legacy ids = %v", ids)
	}
	projects, _ := repo.ListProjects(ctx, s, true)
	var portal repo.Project
	for _, p := range projects {
		if p.Slug == "portal" {
			portal = p
		}
	}
	c, err := repo.GetQACaseByKey(ctx, s, portal.ID, 7)
	if err != nil || !c.CreatedAt.Equal(old) {
		t.Fatalf("case = %+v, %v; want created_at kept", c, err)
	}
	// The next case created normally continues after the imported key.
	next, _ := repo.CreateQACase(ctx, s, portal.ID, repo.QACaseInput{SectionID: c.SectionID, Title: "New"}, nil)
	if next.Key != 8 {
		t.Fatalf("next key = %d; want 8", next.Key)
	}
}
