package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestQACases_TreeFilterSearchAndEscaping(t *testing.T) {
	e := newQAEnv(t, "acme-qacases")
	ctx := context.Background()
	p := e.project(t, "Portal")
	auth, _ := repo.CreateQASection(ctx, e.s, p, "Auth <i>core</i>", nil)
	login, _ := repo.CreateQASection(ctx, e.s, p, "Login", &auth.ID)
	billing, _ := repo.CreateQASection(ctx, e.s, p, "Billing", nil)
	for _, c := range []struct {
		sec   *repo.QASection
		title string
	}{{auth, "Session expires"}, {login, `Login <script>alert(1)</script>`}, {login, "Login invalid"}, {billing, "Pay invoice"}} {
		if _, err := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: c.sec.ID, Title: c.title}, nil); err != nil {
			t.Fatal(err)
		}
	}
	base := "/projects/" + p.String() + "/qa/cases"

	status, body := e.get(t, e.owner, base)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<i>core</i>") ||
		!strings.Contains(body, "Auth &lt;i&gt;core&lt;/i&gt;") {
		t.Fatal("section or case names are not escaped")
	}
	// The tree counts a section's whole subtree.
	if !strings.Contains(body, `data-section-count="`+auth.ID.String()+`">3<`) {
		t.Fatalf("Auth subtree count missing:\n%s", body)
	}
	if strings.Count(body, `data-case-row`) != 4 {
		t.Fatalf("all cases: %d rows; want 4", strings.Count(body, `data-case-row`))
	}

	_, body = e.get(t, e.owner, base+"?section="+auth.ID.String())
	if n := strings.Count(body, `data-case-row`); n != 3 || strings.Contains(body, "Pay invoice") {
		t.Fatalf("Auth selected: %d rows; want its 3 cases including Login's", n)
	}
	_, body = e.get(t, e.owner, base+"?section="+auth.ID.String()+"&q=invalid")
	if n := strings.Count(body, `data-case-row`); n != 1 || !strings.Contains(body, "Login invalid") {
		t.Fatalf("search in Auth: %d rows", n)
	}
	if !strings.Contains(body, `href="`+base+`/3"`) || !strings.Contains(body, "TC-3") {
		t.Fatal("the case row does not show its key or link to the case")
	}
}

func TestQACases_ArchivedToggle(t *testing.T) {
	e := newQAEnv(t, "acme-qacases-arch")
	ctx := context.Background()
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "Old", nil)
	c, _ := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: "Legacy flow"}, nil)
	repo.ArchiveQASection(ctx, e.s, sec.ID)
	base := "/projects/" + p.String() + "/qa/cases"

	_, body := e.get(t, e.owner, base)
	if strings.Contains(body, "Legacy flow") || strings.Contains(body, ">Old<") {
		t.Fatal("archived section or case shown by default")
	}
	_, body = e.get(t, e.owner, base+"?archived=1")
	if !strings.Contains(body, "Legacy flow") || !strings.Contains(body, "Old") {
		t.Fatalf("archived=1 hides archived rows:\n%s", body)
	}
	_ = c
}

func TestQACases_Pages(t *testing.T) {
	e := newQAEnv(t, "acme-qacases-page")
	ctx := context.Background()
	p := e.project(t, "Portal")
	sec, _ := repo.CreateQASection(ctx, e.s, p, "All", nil)
	for i := 1; i <= qaCasesPerPage+5; i++ {
		if _, err := repo.CreateQACase(ctx, e.s, p, repo.QACaseInput{SectionID: sec.ID, Title: fmt.Sprintf("Case %03d", i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	base := "/projects/" + p.String() + "/qa/cases"
	_, body := e.get(t, e.owner, base+"?page=2")
	if n := strings.Count(body, `data-case-row`); n != 5 {
		t.Fatalf("page 2: %d rows; want 5", n)
	}
	if !strings.Contains(body, fmt.Sprintf("%d–%d of %d cases", qaCasesPerPage+1, qaCasesPerPage+5, qaCasesPerPage+5)) {
		t.Fatalf("pager text missing:\n%s", body)
	}
	// Out of range pages clamp rather than showing an empty table.
	_, body = e.get(t, e.owner, base+"?page=99")
	if n := strings.Count(body, `data-case-row`); n != 5 {
		t.Fatalf("page 99: %d rows; want the last page", n)
	}
}

func TestQACases_SectionOfAnotherProjectIs404(t *testing.T) {
	e := newQAEnv(t, "acme-qacases-404")
	ctx := context.Background()
	p := e.project(t, "Portal")
	other := e.project(t, "Billing")
	foreign, _ := repo.CreateQASection(ctx, e.s, other, "Invoices", nil)
	for _, q := range []string{"?section=" + foreign.ID.String(), "?section=not-a-uuid"} {
		if status, _ := e.get(t, e.owner, "/projects/"+p.String()+"/qa/cases"+q); status != http.StatusNotFound {
			t.Fatalf("%s = %d; want 404", q, status)
		}
	}
}
