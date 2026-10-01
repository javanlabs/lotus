package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// Import is two requests. The upload is parsed and checked against the
// project's sections and nothing is written; the preview says what would
// happen and, when every row is valid, carries the file's text in its
// confirm form. The confirm parses and checks that text again — it is
// client input like any other — and writes every case in one transaction.

type qaImportData struct {
	Project     repo.Project
	Base        string
	Previewed   bool
	FileError   string
	RowErrors   []string
	Cases       int
	NewSections []string
	MoreNew     int // new sections beyond the ones listed
	CSV         string
}

const qaImportListedSections = 20

// qaImportCheck compares parsed rows with the project's sections: which
// paths would be created, and which rows run through an archived section.
func qaImportCheck(rows []qaCSVRow, sections []repo.QASection) (newPaths, rowErrs []string) {
	archived := map[string]string{} // lower path -> display path
	live := map[string]bool{}
	for _, r := range qaSectionTree(sections, nil, true, nil) {
		if r.Archived {
			archived[strings.ToLower(r.Path)] = r.Path
		} else {
			live[strings.ToLower(r.Path)] = true
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		for i := range row.SectionPath {
			display := strings.Join(row.SectionPath[:i+1], " / ")
			key := strings.ToLower(display)
			if p, ok := archived[key]; ok {
				rowErrs = append(rowErrs, fmt.Sprintf("Row %d: section '%s' is archived.", row.Line, p))
				break
			}
			if !live[key] && !seen[key] {
				seen[key] = true
				newPaths = append(newPaths, display)
			}
		}
	}
	return newPaths, rowErrs
}

// qaCheckPICs resolves each row's pic email to someone who can be PIC
// here; pics is in row order (nil where a row names nobody).
func qaCheckPICs(people []qaPerson, rows []qaCSVRow) (pics []*uuid.UUID, rowErrs []string) {
	pics = make([]*uuid.UUID, len(rows))
	for i, row := range rows {
		id, err := qaPICByEmail(people, row.PIC)
		if err != nil {
			rowErrs = append(rowErrs, fmt.Sprintf("Row %d: pic '%s' is not someone who can be PIC here.", row.Line, row.PIC))
		}
		pics[i] = id
	}
	return pics, rowErrs
}

// previewQAImport parses csv and fills the preview. ok is false when the
// file or any row is rejected. pics is each row's PIC (see qaCheckPICs).
func previewQAImport(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, csv string) (qaImportData, []qaCSVRow, []*uuid.UUID, bool, error) {
	data := qaImportData{Project: *project, Base: qaBase(project), Previewed: true}
	rows, rowErrs, err := parseQACaseCSV(strings.NewReader(csv))
	if err != nil {
		data.FileError = err.Error()
		return data, nil, nil, false, nil
	}
	sections, err := repo.ListQASections(c.Context(), s, project.ID)
	if err != nil {
		return data, nil, nil, false, err
	}
	people, err := qaPeople(c, d, s, project)
	if err != nil {
		return data, nil, nil, false, err
	}
	newPaths, archivedErrs := qaImportCheck(rows, sections)
	pics, picErrs := qaCheckPICs(people, rows)
	data.RowErrors = append(append(rowErrs, archivedErrs...), picErrs...)
	data.Cases = len(rows)
	if len(newPaths) > qaImportListedSections {
		data.MoreNew = len(newPaths) - qaImportListedSections
		newPaths = newPaths[:qaImportListedSections]
	}
	data.NewSections = newPaths
	ok := len(data.RowErrors) == 0 && len(rows) > 0
	if len(rows) == 0 && len(data.RowErrors) == 0 {
		data.FileError = "The file has no cases."
	}
	if ok {
		data.CSV = csv
	}
	return data, rows, pics, ok, nil
}

func renderQAImport(c *fiber.Ctx, d Deps, s tenant.Scope, data qaImportData, status int) error {
	org, user, err := shellChrome(c, d.DB)
	if err != nil {
		return err
	}
	nav, err := qaNav(c, s, &data.Project, httpx.NavCrumb{Label: "Cases", Href: data.Base + "/cases"}, httpx.NavCrumb{Label: "Import"})
	if err != nil {
		return writeRepoErr(c, err)
	}
	c.Type("html")
	c.Status(status)
	return renderPage(c, d, "pages/qa_case_import.html", httpx.ViewData{
		Title: "Import cases · " + data.Project.Name, Org: org, User: user, Nav: nav, Data: data,
	})
}

func qaImportPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		return renderQAImport(c, d, s, qaImportData{Project: *project, Base: qaBase(project)}, fiber.StatusOK)
	}
}

func previewQAImportUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		fh, err := c.FormFile("file")
		if err != nil {
			data := qaImportData{Project: *project, Base: qaBase(project), Previewed: true, FileError: "Choose a CSV file."}
			return renderQAImport(c, d, s, data, fiber.StatusUnprocessableEntity)
		}
		f, err := fh.Open()
		if err != nil {
			return err
		}
		defer f.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, f); err != nil {
			return err
		}
		data, _, _, ok, err := previewQAImport(c, d, s, project, buf.String())
		if err != nil {
			return writeRepoErr(c, err)
		}
		status := fiber.StatusOK
		if !ok {
			status = fiber.StatusUnprocessableEntity
		}
		return renderQAImport(c, d, s, data, status)
	}
}

func confirmQAImportUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		data, rows, pics, ok, err := previewQAImport(c, d, s, project, c.FormValue("csv"))
		if err != nil {
			return writeRepoErr(c, err)
		}
		if !ok {
			return renderQAImport(c, d, s, data, fiber.StatusUnprocessableEntity)
		}
		in := make([]repo.QACaseImport, len(rows))
		for i, r := range rows {
			in[i] = repo.QACaseImport{SectionPath: r.SectionPath, Case: repo.QACaseInput{
				Title: r.Title, Precondition: r.Precondition, Steps: r.Steps, Priority: r.Priority, Negative: r.Negative,
				PicID: pics[i],
			}}
		}
		n, _, err := repo.ImportQACases(c.Context(), s, project.ID, in, qaActor(c))
		var archived *repo.QASectionArchivedError
		if errors.As(err, &archived) { // archived between preview and confirm
			data.CSV, data.RowErrors = "", []string{fmt.Sprintf("Section '%s' is archived.", archived.Path)}
			return renderQAImport(c, d, s, data, fiber.StatusUnprocessableEntity)
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.cases.imported", fmt.Sprint(n))
		return c.Redirect(qaBase(project)+"/cases", fiber.StatusFound)
	}
}

// exportQACasesUI is GET /projects/:id/qa/cases/export[?section=]: the live
// cases of the section's subtree (or the whole project) in the import format.
func exportQACasesUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		filter := repo.QACaseFilter{IncludeSubsections: true}
		if raw := c.Query("section"); raw != "" {
			id, err := qaOwned(c, s, project, repo.QAKindSection, raw)
			if err != nil {
				return err
			}
			filter.SectionID = &id
		}
		cases, err := repo.ListQACases(c.Context(), s, project.ID, filter)
		if err != nil {
			return writeRepoErr(c, err)
		}
		sections, err := repo.ListQASections(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		paths := map[uuid.UUID][]string{}
		for _, r := range qaSectionTree(sections, nil, true, nil) {
			paths[r.ID] = strings.Split(r.Path, " / ")
		}
		pics, err := qaPICEmails(c, d, cases)
		if err != nil {
			return writeRepoErr(c, err)
		}
		rows := make([]qaCSVRow, len(cases))
		for i, qc := range cases {
			rows[i] = qaCSVRow{
				SectionPath: paths[qc.SectionID], Title: qc.Title, Precondition: qc.Precondition,
				Steps: qc.Steps, Priority: qc.Priority, Negative: qc.Negative, PIC: pics[qc.ID],
			}
		}
		var buf bytes.Buffer
		if err := writeQACaseCSV(&buf, rows); err != nil {
			return err
		}
		c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s-cases.csv"`, project.Slug))
		return c.Send(buf.Bytes())
	}
}
