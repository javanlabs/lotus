package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// qaPICEnv is a project with a member (Ana, who can be PIC), a viewer
// (who cannot) and a Checkout > Card section tree.
type qaPICEnv struct {
	qaEnv
	p               uuid.UUID
	base            string
	ana, viewer     string
	anaID, viewerID uuid.UUID
	checkout, card  *repo.QASection
}

func newQAPICEnv(t *testing.T, sub string) qaPICEnv {
	t.Helper()
	e := qaPICEnv{qaEnv: newQAEnv(t, sub)}
	e.p = e.project(t, "Portal")
	e.base = e.projectPath(t, e.p) + "/qa"
	e.ana, e.anaID = e.session(t, "ana@acme.example", "member")
	e.viewer, e.viewerID = e.session(t, "viewer@acme.example", "viewer")
	ctx := context.Background()
	e.checkout, _ = repo.CreateQASection(ctx, e.s, e.p, "Checkout", nil)
	e.card, _ = repo.CreateQASection(ctx, e.s, e.p, "Card", &e.checkout.ID)
	return e
}

func TestQAPIC_SectionCreateAndEditSetThePIC(t *testing.T) {
	e := newQAPICEnv(t, "acme-pic-sec")

	resp, _ := e.post(t, e.ana, e.base+"/sections", url.Values{"name": {"Auth"}, "pic_id": {e.anaID.String()}}, false)
	auth := qaSectionNamed(t, e.qaEnv, e.p, "Auth")
	if resp.StatusCode != http.StatusFound || auth.PicID == nil || *auth.PicID != e.anaID {
		t.Fatalf("create with PIC = %d, PIC %v", resp.StatusCode, auth.PicID)
	}
	// A viewer cannot be PIC: refused in the dialog, nothing created.
	resp, body := e.post(t, e.ana, e.base+"/sections", url.Values{"name": {"Billing"}, "pic_id": {e.viewerID.String()}}, true)
	if resp.StatusCode != http.StatusOK || !hasText(body, "That person can't be the PIC here.") {
		t.Fatalf("viewer as PIC = %d %s", resp.StatusCode, body)
	}
	if secs, _ := repo.ListQASections(context.Background(), e.s, e.p); len(secs) != 3 {
		t.Fatalf("sections = %d; the refused one was created", len(secs))
	}

	// The edit dialog saves the name and the PIC together; a rename that
	// sends no PIC field leaves it alone.
	path := e.base + "/sections/" + auth.ID.String() + "/rename"
	if resp, _ := e.post(t, e.ana, path, url.Values{"name": {"Sign in"}}, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename = %d", resp.StatusCode)
	}
	if got := qaSectionNamed(t, e.qaEnv, e.p, "Sign in"); got.PicID == nil {
		t.Fatal("a rename without a PIC field cleared the PIC")
	}
	if resp, _ := e.post(t, e.ana, path, url.Values{"name": {"Sign in"}, "pic_id": {""}}, true); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("clear PIC = %d", resp.StatusCode)
	}
	if got := qaSectionNamed(t, e.qaEnv, e.p, "Sign in"); got.PicID != nil {
		t.Fatal("PIC not cleared")
	}
	_, body = e.post(t, e.ana, path, url.Values{"name": {"Sign in"}, "pic_id": {uuid.NewString()}}, true)
	if !hasText(body, "That person can't be the PIC here.") {
		t.Fatalf("unknown PIC = %s", body)
	}

	// The cases page names the chosen section's PIC, inherited or its own.
	repo.SetQASectionPIC(context.Background(), e.s, e.checkout.ID, &e.anaID)
	_, body = e.get(t, e.ana, e.base+"/cases?section="+e.card.ID.String())
	if !strings.Contains(body, "PIC ana@acme.example (from section Checkout)") {
		t.Fatalf("inherited section PIC missing:\n%s", body)
	}
	_, body = e.get(t, e.ana, e.base+"/cases?section="+e.checkout.ID.String())
	if !strings.Contains(body, "PIC ana@acme.example") || strings.Contains(body, "(from section") {
		t.Fatal("own section PIC shown as inherited, or missing")
	}
}

func TestQAPIC_CaseFormAndPage(t *testing.T) {
	e := newQAPICEnv(t, "acme-pic-case")
	ctx := context.Background()
	repo.SetQASectionPIC(ctx, e.s, e.checkout.ID, &e.anaID)

	form := url.Values{"title": {"Pay by card"}, "section_id": {e.card.ID.String()}, "pic_id": {e.viewerID.String()}}
	resp, body := e.post(t, e.ana, e.base+"/cases/new", form, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !hasText(body, "That person can't be the PIC here.") {
		t.Fatalf("viewer as case PIC = %d", resp.StatusCode)
	}
	form.Set("pic_id", "")
	if resp, _ := e.post(t, e.ana, e.base+"/cases/new", form, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	qc, _ := repo.GetQACaseByKey(ctx, e.s, e.p, 1)
	if qc.PicID != nil {
		t.Fatal("no PIC chosen, yet the case has one")
	}
	// Inherited from Checkout, and its author shown.
	_, body = e.get(t, e.viewer, e.base+"/cases/1")
	if !strings.Contains(body, "ana@acme.example (from section Checkout)") || !strings.Contains(body, "created by ana@acme.example") {
		t.Fatalf("case page lacks inherited PIC or author:\n%s", body)
	}

	form.Set("pic_id", e.anaID.String())
	if resp, _ := e.post(t, e.ana, e.base+"/cases/1", form, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	qc, _ = repo.GetQACaseByKey(ctx, e.s, e.p, 1)
	if qc.PicID == nil || *qc.PicID != e.anaID {
		t.Fatal("case PIC not saved")
	}
	_, body = e.get(t, e.ana, e.base+"/cases/1")
	if strings.Contains(body, "(from section") || !strings.Contains(body, `name="pic_id" value="`+e.anaID.String()+`"`) {
		t.Fatalf("own PIC shown as inherited, or not chosen in the editor:\n%s", body)
	}

	// A PIC who became a viewer since stays on the case and does not block saving it.
	if _, err := e.db.Exec(`UPDATE memberships SET role_id = (SELECT id FROM roles WHERE key = 'viewer' AND org_id IS NULL)
		WHERE user_id = $1`, e.anaID); err != nil {
		t.Fatal(err)
	}
	form.Set("title", "Pay by card (3DS)")
	if resp, _ := e.post(t, e.owner, e.base+"/cases/1", form, false); resp.StatusCode != http.StatusFound {
		t.Fatalf("save keeping a former PIC = %d", resp.StatusCode)
	}
}

func TestQAPIC_CaseListColumnAndFilter(t *testing.T) {
	e := newQAPICEnv(t, "acme-pic-list")
	ctx := context.Background()
	repo.SetQASectionPIC(ctx, e.s, e.checkout.ID, &e.anaID)
	var ownerID uuid.UUID
	e.db.Get(&ownerID, `SELECT user_id FROM memberships m JOIN roles r ON r.id = m.role_id WHERE m.org_id = $1 AND r.key = 'owner'`, e.orgID)
	other, _ := repo.CreateQASection(ctx, e.s, e.p, "Auth", nil)
	repo.CreateQACase(ctx, e.s, e.p, repo.QACaseInput{SectionID: e.card.ID, Title: "Inherited case"}, nil)
	repo.CreateQACase(ctx, e.s, e.p, repo.QACaseInput{SectionID: other.ID, Title: "Owner case", PicID: &ownerID}, nil)
	repo.CreateQACase(ctx, e.s, e.p, repo.QACaseInput{SectionID: other.ID, Title: "Orphan case"}, nil)

	_, body := e.get(t, e.ana, e.base+"/cases")
	if !strings.Contains(body, ">PIC<") || !strings.Contains(body, `title="From section Checkout"`) {
		t.Fatalf("PIC column or inherited marker missing:\n%s", body)
	}
	rows := func(query string) int {
		t.Helper()
		status, body := e.get(t, e.ana, e.base+"/cases?"+query)
		if status != http.StatusOK {
			t.Fatalf("%s = %d", query, status)
		}
		return strings.Count(body, "data-case-row")
	}
	if n := rows("pic=me"); n != 1 {
		t.Fatalf("pic=me (Ana, inherited) = %d rows; want 1", n)
	}
	if n := rows("pic=" + ownerID.String()); n != 1 {
		t.Fatalf("pic=owner = %d rows; want 1", n)
	}
	if n := rows("pic=none"); n != 1 {
		t.Fatalf("pic=none = %d rows; want 1", n)
	}
	if status, _ := e.get(t, e.ana, e.base+"/cases?pic=nonsense"); status != http.StatusNotFound {
		t.Fatalf("pic=nonsense = %d; want 404", status)
	}
	// The filter carries over to the section links.
	_, body = e.get(t, e.ana, e.base+"/cases?pic=me")
	if !strings.Contains(body, "section="+e.card.ID.String()+"&amp;pic=me") {
		t.Fatalf("section links drop the PIC filter:\n%s", body)
	}
}
