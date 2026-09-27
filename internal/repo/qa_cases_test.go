package repo_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

func mustQACase(t *testing.T, s tenant.Scope, projectID, sectionID uuid.UUID, title string) *repo.QACase {
	t.Helper()
	c, err := repo.CreateQACase(context.Background(), s, projectID, repo.QACaseInput{
		SectionID: sectionID, Title: title,
		Steps: repo.QASteps{{Action: "Open " + title, Expected: "Shown"}},
	}, nil)
	if err != nil {
		t.Fatalf("create case %q: %v", title, err)
	}
	return c
}

func TestQACases_KeysAreSequentialPerProjectEvenUnderConcurrency(t *testing.T) {
	db := dbtest.Open(t)
	_, s := folderTestOrg(t, db, "acme-case")
	p1 := mustProject(t, s, "portal", nil)
	p2 := mustProject(t, s, "mobile", nil)
	sec1, _ := repo.CreateQASection(context.Background(), s, p1.ID, "Auth", nil)
	sec2, _ := repo.CreateQASection(context.Background(), s, p2.ID, "Auth", nil)

	var wg sync.WaitGroup
	keys := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := repo.CreateQACase(context.Background(), s, p1.ID, repo.QACaseInput{SectionID: sec1.ID, Title: "t"}, nil)
			if err != nil {
				t.Errorf("concurrent create: %v", err)
				return
			}
			keys <- c.Key
		}()
	}
	wg.Wait()
	close(keys)
	seen := map[int]bool{}
	for k := range keys {
		if seen[k] {
			t.Errorf("duplicate key %d", k)
		}
		seen[k] = true
	}
	if len(seen) != 10 || !seen[1] || !seen[10] {
		t.Errorf("keys = %v; want 1..10", seen)
	}
	// Another project starts at 1 again.
	if c := mustQACase(t, s, p2.ID, sec2.ID, "x"); c.Key != 1 {
		t.Errorf("other project first key = %d; want 1", c.Key)
	}
}

func TestQACases_VersionBumpsOnlyOnContentChange(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-ver")
	p := mustProject(t, s, "portal", nil)
	sec, _ := repo.CreateQASection(ctx, s, p.ID, "Auth", nil)
	other, _ := repo.CreateQASection(ctx, s, p.ID, "Profile", nil)
	c := mustQACase(t, s, p.ID, sec.ID, "Login")

	in := repo.QACaseInput{SectionID: other.ID, Title: c.Title, Steps: c.Steps, Notes: "moved"}
	c2, err := repo.UpdateQACase(ctx, s, c.ID, in)
	if err != nil || c2.Version != 1 || c2.SectionID != other.ID {
		t.Fatalf("notes/section change: version=%d section=%v err=%v; want 1, moved", c2.Version, c2.SectionID, err)
	}
	in.Steps = repo.QASteps{{Action: "Open login", Expected: "Form shown"}}
	c3, _ := repo.UpdateQACase(ctx, s, c.ID, in)
	if c3.Version != 2 {
		t.Fatalf("steps change: version=%d; want 2", c3.Version)
	}
}

func TestQACases_RejectsSectionOfAnotherProject(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-xsec")
	p1 := mustProject(t, s, "portal", nil)
	p2 := mustProject(t, s, "mobile", nil)
	sec2, _ := repo.CreateQASection(ctx, s, p2.ID, "Auth", nil)
	_, err := repo.CreateQACase(ctx, s, p1.ID, repo.QACaseInput{SectionID: sec2.ID, Title: "t"}, nil)
	if err != repo.ErrNotFound {
		t.Fatalf("case in other project's section: %v; want ErrNotFound", err)
	}
}
