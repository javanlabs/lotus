package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAPIC_CSVExportAndImportByEmail(t *testing.T) {
	t.Parallel()
	e := newQAPICEnv(t, "acme-pic-csv")
	ctx := context.Background()
	repo.CreateQACase(ctx, e.s, e.p, repo.QACaseInput{SectionID: e.card.ID, Title: "Pay", PicID: &e.anaID}, nil)
	repo.CreateQACase(ctx, e.s, e.p, repo.QACaseInput{SectionID: e.card.ID, Title: "Refund"}, nil)

	resp, err := e.app.Test(reqWithSession(http.MethodGet, e.host, e.base+"/cases/export", e.ana), -1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	csv := string(raw)
	if !strings.HasPrefix(csv, "section,title,precondition,steps,expected,priority,negative,pic\n") ||
		!strings.Contains(csv, "Pay,,,,,,ana@acme.example\n") || !strings.Contains(csv, "Refund,,,,,,\n") {
		t.Fatalf("export =\n%s", csv)
	}

	to := e.project(t, "Portal copy")
	imp := e.projectPath(t, to) + "/qa/cases/import"
	_, body := e.upload(t, e.ana, imp, csv)
	e.post(t, e.ana, imp+"/confirm", url.Values{"csv": {confirmField(t, body)}}, false)
	got, _ := repo.ListQACases(ctx, e.s, to, repo.QACaseFilter{})
	if len(got) != 2 || got[0].PicID == nil || *got[0].PicID != e.anaID || got[1].PicID != nil {
		t.Fatalf("imported = %+v", got)
	}

	resp, body = e.upload(t, e.ana, imp, qaCSVHead[:len(qaCSVHead)-1]+",pic\nAuth,A,,,,,,nobody@acme.example\nAuth,B,,,,,,viewer@acme.example\n")
	if resp.StatusCode != http.StatusUnprocessableEntity ||
		!hasText(body, "Row 2: pic 'nobody@acme.example' is not someone who can be PIC here.") ||
		!hasText(body, "Row 3: pic 'viewer@acme.example' is not someone who can be PIC here.") {
		t.Fatalf("unknown PIC = %d:\n%s", resp.StatusCode, body)
	}
}

func TestQAPIC_APIShowsAndSetsThePIC(t *testing.T) {
	t.Parallel()
	e := newQAAPIEnv(t, "acme-pic-api")
	ctx := context.Background()
	path := "/api/qa/projects/" + e.projectID.String() + "/cases"
	rows := apiCaseRows(2, "PIC")
	rows[0]["pic"] = "QA@acme.example"
	status, body := e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": rows})
	got := decodeJSON[apiCreatedCases](t, body)
	if status != http.StatusCreated || got.Created != 2 {
		t.Fatalf("create with pic = %d %s", status, body)
	}
	bad := apiCaseRows(1, "PIC")
	bad[0]["pic"] = "stranger@example.com"
	status, body = e.api(t, http.MethodPost, e.memberToken, path, map[string]any{"cases": bad})
	if got := decodeJSON[apiCreatedCases](t, body); status != http.StatusUnprocessableEntity || len(got.Errors) != 1 ||
		got.Errors[0] != "Row 1: pic 'stranger@example.com' is not someone who can be PIC here." {
		t.Fatalf("unknown pic = %d %s", status, body)
	}

	sections, _ := repo.ListQASections(ctx, e.s, e.projectID)
	for _, s := range sections {
		if s.Name == "PIC" {
			repo.SetQASectionPIC(ctx, e.s, s.ID, &e.guestID)
		}
	}
	status, body = e.api(t, http.MethodGet, e.memberToken, path+"?q=Generated", nil)
	type user struct {
		ID    uuid.UUID `json:"id"`
		Email string    `json:"email"`
	}
	list := decodeJSON[struct {
		Cases []struct {
			Title        string `json:"title"`
			PIC          *user  `json:"pic"`
			EffectivePIC *user  `json:"effective_pic"`
			CreatedBy    *user  `json:"created_by"`
		} `json:"cases"`
	}](t, body)
	if status != http.StatusOK || len(list.Cases) != 2 {
		t.Fatalf("list = %d %s", status, body)
	}
	a, b := list.Cases[0], list.Cases[1]
	if a.PIC == nil || a.PIC.ID != e.memberID || a.PIC.Email != "qa@acme.example" || a.EffectivePIC.ID != e.memberID ||
		a.CreatedBy == nil || a.CreatedBy.ID != e.memberID {
		t.Fatalf("own PIC case = %s", body)
	}
	if b.PIC != nil || b.EffectivePIC == nil || b.EffectivePIC.ID != e.guestID {
		t.Fatalf("inherited PIC case = %s", body)
	}

	// MCP create_cases takes the same field.
	res := e.callTool(t, e.memberToken, "create_cases", map[string]any{"project_id": e.projectID.String(),
		"cases": []map[string]any{{"section_path": []string{"PIC"}, "title": "Via MCP", "pic": "client@bank.example"}}})
	if res.IsError {
		t.Fatalf("MCP create with pic = %+v", res)
	}
}
