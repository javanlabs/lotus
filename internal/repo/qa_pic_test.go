package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
)

func picUser(t *testing.T, db *sqlx.DB, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Get(&id, `INSERT INTO users (email, name) VALUES ($1, $1) RETURNING id`, email); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func TestQAPIC_SectionAndCaseKeepTheirPIC(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-pic")
	p := mustProject(t, s, "portal", nil)
	ana := picUser(t, db, "ana@example.com")
	budi := picUser(t, db, "budi@example.com")

	sec, _ := repo.CreateQASection(ctx, s, p.ID, "Checkout", nil)
	if err := repo.SetQASectionPIC(ctx, s, sec.ID, &ana); err != nil {
		t.Fatal(err)
	}
	secs, _ := repo.ListQASections(ctx, s, p.ID)
	if secs[0].PicID == nil || *secs[0].PicID != ana {
		t.Fatalf("section PIC = %v; want ana", secs[0].PicID)
	}
	if err := repo.SetQASectionPIC(ctx, s, uuid.New(), &ana); err != repo.ErrNotFound {
		t.Fatalf("missing section: %v; want ErrNotFound", err)
	}

	c, err := repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: sec.ID, Title: "Pay", PicID: &budi}, nil)
	if err != nil || c.PicID == nil || *c.PicID != budi {
		t.Fatalf("created case PIC = %+v, %v; want budi", c, err)
	}
	c, err = repo.UpdateQACase(ctx, s, c.ID, repo.QACaseInput{SectionID: sec.ID, Title: "Pay"})
	if err != nil || c.PicID != nil || c.Version != 1 {
		t.Fatalf("cleared case PIC = %+v, %v; want nil and no version bump", c, err)
	}

	// Deleting a user leaves the rows, without a PIC.
	if err := repo.SetQASectionPIC(ctx, s, sec.ID, &budi); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, budi); err != nil {
		t.Fatal(err)
	}
	secs, _ = repo.ListQASections(ctx, s, p.ID)
	if secs[0].PicID != nil {
		t.Fatal("section PIC survived the user's deletion")
	}
}

func TestQAPIC_EffectivePICIsTheNearestOne(t *testing.T) {
	ana, budi := uuid.New(), uuid.New()
	root := repo.QASection{ID: uuid.New(), Name: "Checkout", PicID: &ana}
	mid := repo.QASection{ID: uuid.New(), ParentID: &root.ID, Name: "Payment"}
	leaf := repo.QASection{ID: uuid.New(), ParentID: &mid.ID, Name: "Card"}
	other := repo.QASection{ID: uuid.New(), Name: "Auth"}
	sections := []repo.QASection{leaf, mid, root, other}

	if pic, from := repo.QAEffectivePIC(sections, leaf.ID, &budi); pic == nil || *pic != budi || from != nil {
		t.Fatalf("own PIC = %v from %v; want budi, not inherited", pic, from)
	}
	if pic, from := repo.QAEffectivePIC(sections, leaf.ID, nil); pic == nil || *pic != ana || from == nil || from.ID != root.ID {
		t.Fatalf("inherited PIC = %v from %v; want ana from Checkout", pic, from)
	}
	if pic, from := repo.QAEffectivePIC(sections, other.ID, nil); pic != nil || from != nil {
		t.Fatalf("no PIC anywhere = %v from %v; want none", pic, from)
	}
}

func TestQAPIC_CaseFilterUsesTheEffectivePIC(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-pic-filter")
	p := mustProject(t, s, "portal", nil)
	ana := picUser(t, db, "ana-f@example.com")
	budi := picUser(t, db, "budi-f@example.com")

	checkout, _ := repo.CreateQASection(ctx, s, p.ID, "Checkout", nil)
	card, _ := repo.CreateQASection(ctx, s, p.ID, "Card", &checkout.ID)
	auth, _ := repo.CreateQASection(ctx, s, p.ID, "Auth", nil)
	repo.SetQASectionPIC(ctx, s, checkout.ID, &ana)
	repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: card.ID, Title: "Inherits ana"}, nil)
	repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: card.ID, Title: "Own budi", PicID: &budi}, nil)
	repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: auth.ID, Title: "Nobody"}, nil)
	repo.CreateQACase(ctx, s, p.ID, repo.QACaseInput{SectionID: auth.ID, Title: "Own ana", PicID: &ana}, nil)

	titles := func(f repo.QACaseFilter) []string {
		t.Helper()
		cases, err := repo.ListQACases(ctx, s, p.ID, f)
		if err != nil {
			t.Fatal(err)
		}
		n, err := repo.CountQACases(ctx, s, p.ID, f)
		if err != nil || n != len(cases) {
			t.Fatalf("count = %d, %v; list has %d", n, err, len(cases))
		}
		var out []string
		for _, c := range cases {
			out = append(out, c.Title)
		}
		return out
	}
	if got := titles(repo.QACaseFilter{PICID: &ana}); len(got) != 2 || got[0] != "Inherits ana" || got[1] != "Own ana" {
		t.Fatalf("PIC ana = %v; want the inherited and the own case", got)
	}
	if got := titles(repo.QACaseFilter{PICID: &budi}); len(got) != 1 || got[0] != "Own budi" {
		t.Fatalf("PIC budi = %v; a case's own PIC overrides its section's", got)
	}
	if got := titles(repo.QACaseFilter{PICNone: true}); len(got) != 1 || got[0] != "Nobody" {
		t.Fatalf("no PIC = %v", got)
	}
	// Combined with a section and paging.
	if got := titles(repo.QACaseFilter{PICID: &ana, SectionID: &checkout.ID, IncludeSubsections: true, Limit: 1}); len(got) != 1 || got[0] != "Inherits ana" {
		t.Fatalf("PIC ana in Checkout = %v", got)
	}
}

func TestQAPIC_ImportKeepsThePIC(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	_, s := folderTestOrg(t, db, "acme-pic-import")
	p := mustProject(t, s, "portal", nil)
	ana := picUser(t, db, "ana-i@example.com")
	keys, _, err := repo.ImportQACasesKeys(ctx, s, p.ID, []repo.QACaseImport{
		{SectionPath: []string{"Auth"}, Case: repo.QACaseInput{Title: "A", PicID: &ana}},
		{SectionPath: []string{"Auth"}, Case: repo.QACaseInput{Title: "B"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := repo.GetQACaseByKey(ctx, s, p.ID, keys[0])
	b, _ := repo.GetQACaseByKey(ctx, s, p.ID, keys[1])
	if a.PicID == nil || *a.PicID != ana || b.PicID != nil {
		t.Fatalf("imported PICs = %v, %v; want ana, none", a.PicID, b.PicID)
	}
}
