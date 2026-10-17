package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/repo"
)

func TestParseQACaseCSV_RowsAndErrors(t *testing.T) {
	t.Parallel()
	in := "\xef\xbb\xbfSection,Title,Precondition,Steps,Expected,Priority,Negative\n" +
		"Auth / Login,Login valid,User exists,\"Open portal\nSubmit\",\"Login page\nDashboard\",High,no\n" +
		"Auth,Session expires,,,,,\n" +
		",No section,,,,,\n" +
		"Auth,,,,,,\n" +
		"Auth,Bad priority,,,,urgent,\n" +
		"Auth,Bad negative,,,,,maybe\n" +
		"Auth,'=SUM(A1),,'-1 then enter,,,yes\n"
	rows, errs, err := parseQACaseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if strings.Join(r.SectionPath, "|") != "Auth|Login" || r.Title != "Login valid" || r.Precondition != "User exists" ||
		len(r.Steps) != 2 || r.Steps[1] != (repo.QAStep{Action: "Submit", Expected: "Dashboard"}) || *r.Priority != "high" || r.Negative {
		t.Fatalf("row 1 = %+v", r)
	}
	if rows[1].Priority != nil || len(rows[1].Steps) != 0 {
		t.Fatalf("row 2 = %+v", rows[1])
	}
	// A leading apostrophe that guards a formula is the exporter's; it goes.
	if rows[2].Title != "=SUM(A1)" || rows[2].Steps[0].Action != "-1 then enter" || !rows[2].Negative {
		t.Fatalf("guarded row = %+v", rows[2])
	}
	want := []string{
		"Row 4: section is required.",
		"Row 5: title is required.",
		"Row 6: priority must be high, medium, low or empty.",
		"Row 7: negative must be yes, no or empty.",
	}
	if strings.Join(errs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors =\n%s\nwant\n%s", strings.Join(errs, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseQACaseCSV_SemicolonsAndHeaderChecks(t *testing.T) {
	t.Parallel()
	rows, errs, err := parseQACaseCSV(strings.NewReader("section;title;precondition;steps;expected;priority;negative\nAuth;Login;;;;low;\n"))
	if err != nil || len(errs) != 0 || len(rows) != 1 || *rows[0].Priority != "low" {
		t.Fatalf("semicolon file = %+v %v %v", rows, errs, err)
	}
	if _, _, err := parseQACaseCSV(strings.NewReader("title,section\nx,y\n")); err == nil ||
		err.Error() != "The first row must be the header: section, title, precondition, steps, expected, priority, negative." {
		t.Fatalf("bad header = %v", err)
	}
	if _, _, err := parseQACaseCSV(strings.NewReader("")); err == nil {
		t.Fatal("empty file accepted")
	}
	long := "section,title,precondition,steps,expected,priority,negative\n" + strings.Repeat("Auth,x,,,,,\n", qaCSVMaxRows+1)
	if _, _, err := parseQACaseCSV(strings.NewReader(long)); err == nil || !strings.Contains(err.Error(), "5000") {
		t.Fatalf("oversized file = %v", err)
	}
}

func TestParseQACaseCSV_PICColumnIsOptional(t *testing.T) {
	t.Parallel()
	rows, errs, err := parseQACaseCSV(strings.NewReader("section,title,precondition,steps,expected,priority,negative,PIC\n" +
		"Auth,Login,,,,,, Ana@Example.com \nAuth,Logout,,,,,\n"))
	if err != nil || len(errs) != 0 || len(rows) != 2 || rows[0].PIC != "Ana@Example.com" || rows[1].PIC != "" {
		t.Fatalf("with pic = %+v %v %v", rows, errs, err)
	}
	// An eighth column under another name is not a PIC.
	rows, _, _ = parseQACaseCSV(strings.NewReader("section,title,precondition,steps,expected,priority,negative,owner\nAuth,Login,,,,,,ana@example.com\n"))
	if len(rows) != 1 || rows[0].PIC != "" {
		t.Fatalf("other column = %+v", rows)
	}
}

func TestWriteQACaseCSV_RoundTrips(t *testing.T) {
	t.Parallel()
	high := "high"
	cases := []qaCSVRow{
		{SectionPath: []string{"Auth", "Login"}, Title: "Login valid", Precondition: "User, with comma", Priority: &high,
			Steps: repo.QASteps{{Action: "Open", Expected: "Page"}, {Action: "Submit", Expected: ""}}},
		{SectionPath: []string{"Auth"}, Title: "=HYPERLINK(\"x\")", Negative: true,
			Steps: repo.QASteps{{Action: "Type -1\nthen enter", Expected: "@error shown"}}},
	}
	var buf bytes.Buffer
	if err := writeQACaseCSV(&buf, cases); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `'=HYPERLINK`) || !strings.Contains(buf.String(), `'@error`) {
		t.Fatalf("formulas not guarded:\n%s", buf.String())
	}
	back, errs, err := parseQACaseCSV(&buf)
	if err != nil || len(errs) != 0 || len(back) != 2 {
		t.Fatalf("parse back = %v %v", errs, err)
	}
	if back[0].Title != "Login valid" || back[0].Precondition != "User, with comma" || len(back[0].Steps) != 2 ||
		back[0].Steps[1].Action != "Submit" || *back[0].Priority != "high" {
		t.Fatalf("row 1 back = %+v", back[0])
	}
	// A newline inside one step cannot survive line pairing; it becomes a space.
	if back[1].Title != `=HYPERLINK("x")` || !back[1].Negative || back[1].Steps[0].Action != "Type -1 then enter" ||
		back[1].Steps[0].Expected != "@error shown" {
		t.Fatalf("row 2 back = %+v", back[1])
	}
}
