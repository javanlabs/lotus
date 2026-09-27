package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/repo"
)

const qaCSVHead = "section,title,precondition,steps,expected,priority,negative\n"

// upload posts a CSV file to the import preview.
func (e qaEnv) upload(t *testing.T, raw, path, csv string) (*http.Response, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "cases.csv")
	fw.Write([]byte(csv))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://"+e.host+path, &body)
	req.Host = e.host
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "lotus_session", Value: raw})
	resp, err := e.app.Test(req, 30_000)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// confirmField is the CSV the preview carries into its confirm form.
func confirmField(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<textarea name="csv" hidden>(.*?)</textarea>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("preview has no confirm form:\n%s", body)
	}
	return htmlUnescape(m[1])
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&amp;", "&").Replace(s)
}

func TestQACaseImport_PreviewThenConfirm(t *testing.T) {
	e := newQAEnv(t, "acme-qaimport")
	ctx := context.Background()
	p := e.project(t, "Portal")
	repo.CreateQASection(ctx, e.s, p, "Auth", nil)
	base := "/projects/" + p.String() + "/qa/cases/import"

	if status, body := e.get(t, e.owner, base); status != http.StatusOK || !strings.Contains(body, `type="file"`) {
		t.Fatalf("upload page = %d", status)
	}
	csv := qaCSVHead + "Auth / Login,Login valid,,\"Open\nSubmit\",\"Page\nHome\",high,\n" + "Billing,Pay <b>invoice</b>,,,,,yes\n"
	resp, body := e.upload(t, e.owner, base, csv)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "2 cases") || !strings.Contains(body, "Auth / Login") ||
		!strings.Contains(body, "Billing") || !strings.Contains(body, "Import 2 cases") {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, body)
	}
	if n, _ := repo.CountQACases(ctx, e.s, p, repo.QACaseFilter{}); n != 0 {
		t.Fatal("preview wrote cases")
	}

	resp, _ = e.post(t, e.owner, base+"/confirm", url.Values{"csv": {confirmField(t, body)}}, false)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/projects/"+p.String()+"/qa/cases" {
		t.Fatalf("confirm = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	c, err := repo.GetQACaseByKey(ctx, e.s, p, 2)
	if err != nil || c.Title != "Pay <b>invoice</b>" || !c.Negative {
		t.Fatalf("imported case 2 = %+v, %v", c, err)
	}
}

func TestQACaseImport_RowErrorsBlockTheImport(t *testing.T) {
	e := newQAEnv(t, "acme-qaimport-bad")
	ctx := context.Background()
	p := e.project(t, "Portal")
	old, _ := repo.CreateQASection(ctx, e.s, p, "Old", nil)
	repo.ArchiveQASection(ctx, e.s, old.ID)
	base := "/projects/" + p.String() + "/qa/cases/import"

	resp, body := e.upload(t, e.owner, base, qaCSVHead+"Auth,,,,,,\nOld / Deep,Legacy,,,,,\nAuth,Fine,,,,,\n")
	if resp.StatusCode != http.StatusUnprocessableEntity || strings.Contains(body, `name="csv"`) {
		t.Fatalf("preview with errors = %d; want 422 and no confirm form", resp.StatusCode)
	}
	if !strings.Contains(body, "Row 2: title is required.") || !hasText(body, "Row 3: section 'Old' is archived.") {
		t.Fatalf("errors missing:\n%s", body)
	}
	// Confirming a crafted form re-checks and refuses the same way.
	resp, _ = e.post(t, e.owner, base+"/confirm", url.Values{"csv": {qaCSVHead + "Old,Legacy,,,,,\n"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("confirm with errors = %d; want 422", resp.StatusCode)
	}
	resp, body = e.upload(t, e.owner, base, "title,section\n")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "The first row must be the header") {
		t.Fatalf("bad header = %d", resp.StatusCode)
	}
	if n, _ := repo.CountQACases(ctx, e.s, p, repo.QACaseFilter{IncludeArchived: true}); n != 0 {
		t.Fatalf("cases = %d; want none", n)
	}
}

func TestQACaseExport_RoundTripsIntoAnotherProject(t *testing.T) {
	e := newQAEnv(t, "acme-qaexport")
	ctx := context.Background()
	viewer, _ := e.session(t, "viewer@acme.example", "viewer")
	from := e.project(t, "Portal")
	to := e.project(t, "Portal copy")
	auth, _ := repo.CreateQASection(ctx, e.s, from, "Auth", nil)
	login, _ := repo.CreateQASection(ctx, e.s, from, "Login", &auth.ID)
	other, _ := repo.CreateQASection(ctx, e.s, from, "Billing", nil)
	high := "high"
	repo.CreateQACase(ctx, e.s, from, repo.QACaseInput{SectionID: login.ID, Title: "=Login, \"quoted\"", Precondition: "User\nexists",
		Priority: &high, Steps: repo.QASteps{{Action: "Open", Expected: "-page shows"}, {Action: "Submit", Expected: ""}}}, nil)
	repo.CreateQACase(ctx, e.s, from, repo.QACaseInput{SectionID: auth.ID, Title: "Session expires", Negative: true}, nil)
	repo.CreateQACase(ctx, e.s, from, repo.QACaseInput{SectionID: other.ID, Title: "Pay"}, nil)

	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, "/projects/"+from.String()+"/qa/cases/export?section="+auth.ID.String(), viewer))
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export = %v %v", resp, err)
	}
	csv, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(csv), "Pay") {
		t.Fatal("export of Auth includes another section's case")
	}

	_, body := e.upload(t, e.owner, "/projects/"+to.String()+"/qa/cases/import", string(csv))
	e.post(t, e.owner, "/projects/"+to.String()+"/qa/cases/import/confirm", url.Values{"csv": {confirmField(t, body)}}, false)
	src, _ := repo.ListQACases(ctx, e.s, from, repo.QACaseFilter{SectionID: &auth.ID, IncludeSubsections: true})
	dst, _ := repo.ListQACases(ctx, e.s, to, repo.QACaseFilter{})
	if len(dst) != len(src) {
		t.Fatalf("round trip: %d cases; want %d", len(dst), len(src))
	}
	for i := range src {
		a, b := src[i], dst[i]
		if a.Title != b.Title || a.Precondition != b.Precondition || fmt.Sprint(a.Steps) != fmt.Sprint(b.Steps) ||
			fmt.Sprint(a.Priority != nil) != fmt.Sprint(b.Priority != nil) || a.Negative != b.Negative {
			t.Fatalf("case %d changed:\n%+v\n%+v", i, a, b)
		}
	}
	if resp, _ := e.upload(t, viewer, "/projects/"+to.String()+"/qa/cases/import", string(csv)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer import = %d; want 403", resp.StatusCode)
	}
}

func TestQACaseImport_FiveThousandRowsInOneRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := newQAEnv(t, "acme-qaimport-big")
	p := e.project(t, "Portal")
	var b strings.Builder
	b.WriteString(qaCSVHead)
	for i := 0; i < qaCSVMaxRows; i++ {
		fmt.Fprintf(&b, "Area %d / Flow %d,Case %d,,\"Open\nSubmit\",\"Page\nDone\",medium,\n", i%20, i%7, i)
	}
	base := "/projects/" + p.String() + "/qa/cases/import"
	start := time.Now()
	_, body := e.upload(t, e.owner, base, b.String())
	resp, _ := e.post(t, e.owner, base+"/confirm", url.Values{"csv": {confirmField(t, body)}}, false)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("confirm = %d", resp.StatusCode)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("5000-row import took %s; want under 10s", took)
	}
	if n, _ := repo.CountQACases(context.Background(), e.s, p, repo.QACaseFilter{}); n != qaCSVMaxRows {
		t.Fatalf("imported %d; want %d", n, qaCSVMaxRows)
	}
}
