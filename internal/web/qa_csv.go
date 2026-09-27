package web

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/uyab/lotus/internal/repo"
)

// The case CSV is one case per row:
//
//	section, title, precondition, steps, expected, priority, negative
//
// section is a path ("Auth / Login"); steps and expected hold one step per
// line and are paired by line; priority is high, medium, low or empty;
// negative is yes, no or empty. Export writes the same columns, so an
// exported file imports back unchanged — except that a newline inside a
// single step becomes a space, because lines are what separate steps.
//
// A cell starting with = + - @ is written with a leading apostrophe, so a
// spreadsheet opening the file shows text instead of running a formula; the
// importer takes that apostrophe off again.

var qaCSVHeader = []string{"section", "title", "precondition", "steps", "expected", "priority", "negative"}

// qaCSVMaxRows bounds one import: one request, one transaction.
const qaCSVMaxRows = 5000

type qaCSVRow struct {
	Line         int // the spreadsheet row number; the header is 1
	SectionPath  []string
	Title        string
	Precondition string
	Steps        repo.QASteps
	Priority     *string
	Negative     bool
}

func qaCSVUnguard(cell string) string {
	if len(cell) > 1 && cell[0] == '\'' && strings.ContainsRune("=+-@\t\r", rune(cell[1])) {
		return cell[1:]
	}
	return cell
}

func qaCSVGuard(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

func qaCSVLines(cell string) []string {
	if strings.TrimSpace(cell) == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(cell, "\r\n", "\n"), "\n")
}

// parseQACaseCSV reads a case CSV. err is a problem with the file as a whole
// (header, size, encoding); rowErrs are per-row problems, one sentence each,
// numbered as a spreadsheet numbers rows (the header is row 1).
func parseQACaseCSV(r io.Reader) (rows []qaCSVRow, rowErrs []string, err error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		return nil, nil, errors.New("The file is not UTF-8 text. Save it as CSV UTF-8 and upload it again.")
	}
	first, _, _ := bytes.Cut(raw, []byte("\n"))
	cr := csv.NewReader(bytes.NewReader(raw))
	if bytes.Contains(first, []byte(";")) && !bytes.Contains(first, []byte(",")) {
		cr.Comma = ';'
	}
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	records, err := cr.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("The file could not be read as CSV: %v.", err)
	}
	headerErr := errors.New("The first row must be the header: " + strings.Join(qaCSVHeader, ", ") + ".")
	if len(records) == 0 || len(records[0]) < len(qaCSVHeader) {
		return nil, nil, headerErr
	}
	for i, h := range qaCSVHeader {
		if strings.ToLower(strings.TrimSpace(records[0][i])) != h {
			return nil, nil, headerErr
		}
	}
	if len(records)-1 > qaCSVMaxRows {
		return nil, nil, fmt.Errorf("The file has more than %d rows. Split it and import each part.", qaCSVMaxRows)
	}
	for n, rec := range records[1:] {
		line := n + 2
		cell := func(i int) string {
			if i < len(rec) {
				return qaCSVUnguard(strings.TrimSpace(rec[i]))
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		row := qaCSVRow{Line: line, Title: cell(1), Precondition: cell(2)}
		for _, part := range strings.Split(cell(0), "/") {
			if part = strings.TrimSpace(part); part != "" {
				row.SectionPath = append(row.SectionPath, part)
			}
		}
		fail := func(msg string) { rowErrs = append(rowErrs, fmt.Sprintf("Row %d: %s", line, msg)) }
		switch {
		case len(row.SectionPath) == 0:
			fail("section is required.")
			continue
		case len(row.SectionPath) > repo.MaxQASectionDepth:
			fail(fmt.Sprintf("sections can nest at most %d levels.", repo.MaxQASectionDepth))
			continue
		case row.Title == "":
			fail("title is required.")
			continue
		case utf8.RuneCountInString(row.Title) > repo.MaxQATitleLength:
			fail(fmt.Sprintf("title can be at most %d characters.", repo.MaxQATitleLength))
			continue
		}
		tooLong := false
		for _, p := range row.SectionPath {
			tooLong = tooLong || utf8.RuneCountInString(p) > repo.MaxQANameLength
		}
		if tooLong {
			fail(fmt.Sprintf("section names can be at most %d characters.", repo.MaxQANameLength))
			continue
		}
		switch p := strings.ToLower(cell(5)); p {
		case "":
		case "high", "medium", "low":
			row.Priority = &p
		default:
			fail("priority must be high, medium, low or empty.")
			continue
		}
		switch strings.ToLower(cell(6)) {
		case "", "no", "n", "false", "0":
		case "yes", "y", "true", "1":
			row.Negative = true
		default:
			fail("negative must be yes, no or empty.")
			continue
		}
		var actions, expected []string
		if len(rec) > 3 {
			actions = qaCSVLines(rec[3])
		}
		if len(rec) > 4 {
			expected = qaCSVLines(rec[4])
		}
		row.Steps = repo.QASteps{}
		for i := 0; i < max(len(actions), len(expected)); i++ {
			var a, x string
			if i < len(actions) {
				a = qaCSVUnguard(strings.TrimSpace(actions[i]))
			}
			if i < len(expected) {
				x = qaCSVUnguard(strings.TrimSpace(expected[i]))
			}
			if a != "" || x != "" {
				row.Steps = append(row.Steps, repo.QAStep{Action: a, Expected: x})
			}
		}
		rows = append(rows, row)
	}
	return rows, rowErrs, nil
}

// writeQACaseCSV writes rows in the import format.
func writeQACaseCSV(w io.Writer, rows []qaCSVRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(qaCSVHeader); err != nil {
		return err
	}
	oneLine := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(s, "\r", " ")), " ")
	}
	for _, r := range rows {
		actions := make([]string, len(r.Steps))
		expected := make([]string, len(r.Steps))
		for i, s := range r.Steps {
			actions[i] = qaCSVGuard(oneLine(s.Action))
			expected[i] = qaCSVGuard(oneLine(s.Expected))
		}
		priority, negative := "", ""
		if r.Priority != nil {
			priority = *r.Priority
		}
		if r.Negative {
			negative = "yes"
		}
		if err := cw.Write([]string{
			qaCSVGuard(strings.Join(r.SectionPath, " / ")), qaCSVGuard(r.Title), qaCSVGuard(r.Precondition),
			strings.Join(actions, "\n"), strings.Join(expected, "\n"), priority, negative,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
