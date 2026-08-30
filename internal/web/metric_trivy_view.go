package web

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/clients/harbor"
	trivyclient "github.com/uyab/lotus/internal/clients/trivy"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// trivyCVERow is one parsed vulnerability, optionally carrying a saved
// false-positive annotation — ported from the legacy Lotus's TrivyVuln
// (handlers/trivy.go), minus the AI-draft fields: trivy_finding_notes'
// own ai_generated column is schema-ready but nothing populates it yet
// (see internal/repo/trivy_finding_notes.go's doc comment), so this port
// carries the flag through for display but has no "generate via AI"
// button to set it.
type trivyCVERow struct {
	CVE, Severity, Package, Version, FixVersion, Description, Link, CweIDs string
	SeverityRank                                                           int
	HasNote                                                                bool
	NoteID                                                                 string
	NoteComment                                                            string
	NoteByAI                                                               bool
}

func severityRank(s string) int {
	switch s {
	case "Critical":
		return 4
	case "High":
		return 3
	case "Medium":
		return 2
	case "Low":
		return 1
	default:
		return 0
	}
}

// parseTrivyRawJSON decodes a trivy_snapshots.raw_json blob into CVE
// rows. Two shapes exist — ported verbatim (probe-the-key detection and
// all) from the legacy Lotus's parseSnapshotVulns (handlers/trivy.go):
// Harbor's additions report (registry pulls, keyed "vulnerabilities")
// and Trivy's own CLI report (push ingests, keyed "Results"). Anything
// else (e.g. an ancient snapshot that only ever stored the scan-overview
// summary) yields nil — the header counts still render from the
// snapshot's own columns either way, only the per-CVE table is empty.
func parseTrivyRawJSON(raw []byte) []trivyCVERow {
	if len(raw) == 0 {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	if _, ok := probe["vulnerabilities"]; ok {
		var rep harbor.VulnerabilityReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			return nil
		}
		out := make([]trivyCVERow, 0, len(rep.Vulnerabilities))
		for _, v := range rep.Vulnerabilities {
			link := ""
			if len(v.Links) > 0 {
				link = v.Links[0]
			}
			out = append(out, trivyCVERow{
				CVE: v.ID, Severity: v.Severity, SeverityRank: severityRank(v.Severity),
				Package: v.Package, Version: v.Version, FixVersion: v.FixVersion,
				Description: v.Description, Link: link, CweIDs: strings.Join(v.CweIDs, ", "),
			})
		}
		return out
	}
	if _, ok := probe["Results"]; ok {
		rep, err := trivyclient.ParseFSReport(raw)
		if err != nil {
			return nil
		}
		out := make([]trivyCVERow, 0, len(rep.Vulns))
		for _, v := range rep.Vulns {
			out = append(out, trivyCVERow{
				CVE: v.ID, Severity: v.Severity, SeverityRank: severityRank(v.Severity),
				Package: v.Package, Version: v.Version, FixVersion: v.FixVersion,
				Description: v.Description, Link: v.PrimaryURL, CweIDs: strings.Join(v.CweIDs, ", "),
			})
		}
		return out
	}
	return nil
}

type trivyDetailHeader struct {
	HasData bool

	Source                                               string
	Digest                                               string
	Tags                                                 string
	Severity                                             string
	Total, Critical, High, Medium, Low, Unknown, Fixable int
	ImagePushedAt                                        string
	ScanFinishedAt                                       string
	CapturedAt                                           string
}

func buildTrivyHeader(snap *repo.TrivySnapshot) trivyDetailHeader {
	h := trivyDetailHeader{ImagePushedAt: "–", ScanFinishedAt: "–"}
	if snap == nil {
		return h
	}
	h.HasData = true
	h.Source = snap.Source
	if snap.Digest != nil {
		d := *snap.Digest
		if len(d) > 19 { // "sha256:" + 12 hex chars, matching the legacy shortDigest
			d = d[:19]
		}
		h.Digest = d
	}
	h.Tags = strings.Join([]string(snap.Tags), ", ")
	if snap.Severity != nil {
		h.Severity = *snap.Severity
	}
	h.Total, h.Critical, h.High, h.Medium, h.Low, h.Unknown, h.Fixable =
		snap.Total, snap.Critical, snap.High, snap.Medium, snap.Low, snap.Unknown, snap.Fixable
	if snap.ImagePushedAt != nil {
		h.ImagePushedAt = snap.ImagePushedAt.Local().Format("2006-01-02 15:04")
	}
	if snap.ScanFinishedAt != nil {
		h.ScanFinishedAt = snap.ScanFinishedAt.Local().Format("2006-01-02 15:04")
	}
	h.CapturedAt = relativeTimeStr(snap.CapturedAt)
	return h
}

func renderTrivyDetail(c *fiber.Ctx, d Deps, s tenant.Scope, chrome metricChrome, org *httpx.Org, user *httpx.User, nav *httpx.Nav, flash string) error {
	snap, err := repo.LatestTrivySnapshot(c.Context(), s, chrome.Binding.ID)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return writeRepoErr(c, err)
	}
	if errors.Is(err, repo.ErrNotFound) {
		snap = nil
	}
	header := buildTrivyHeader(snap)

	var rows []trivyCVERow
	if snap != nil {
		rows = parseTrivyRawJSON(snap.RawJSON)
	}
	rawDetailMissing := len(rows) == 0 && header.Total > 0

	notes, err := repo.ListTrivyFindingNotes(c.Context(), s, chrome.Binding.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	byKey := make(map[string]repo.TrivyFindingNote, len(notes))
	for _, n := range notes {
		byKey[n.CVEID+"\x00"+n.Package] = n
	}
	for i := range rows {
		if n, ok := byKey[rows[i].CVE+"\x00"+rows[i].Package]; ok {
			rows[i].HasNote = true
			rows[i].NoteID = n.ID.String()
			rows[i].NoteComment = n.Comment
			rows[i].NoteByAI = n.AIGenerated
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SeverityRank != rows[j].SeverityRank {
			return rows[i].SeverityRank > rows[j].SeverityRank
		}
		return rows[i].CVE < rows[j].CVE
	})

	cards, err := buildMetricChartCards(c.Context(), s, chrome.Binding.ID, chrome.Binding.Kind, "7d")
	if err != nil {
		return writeRepoErr(c, err)
	}

	c.Type("html")
	return renderPage(c, d, "pages/metric_trivy.html", httpx.ViewData{
		Title: chrome.Component.Name + " · Trivy", Org: org, User: user, Nav: nav,
		Data: struct {
			Chrome           metricChrome
			Header           trivyDetailHeader
			Rows             []trivyCVERow
			RawDetailMissing bool
			Flash            string
			Charts           chartGridData
		}{Chrome: chrome, Header: header, Rows: rows, RawDetailMissing: rawDetailMissing, Flash: flash, Charts: newChartGridData(cards, "7d", chrome.basePath())},
	})
}

// saveTrivyNote and deleteTrivyNote are plain form posts (not htmx) that
// redisplay the whole detail page — a false-positive annotation is an
// infrequent, deliberate action, not the kind of live-typing surface
// htmx's partial swaps are for, so a full round-trip keeps this simple
// rather than introducing a fifth htmx endpoint whose result has to stay
// in sync with the sort order every other row already reflects.
func saveTrivyNote() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		cveID := strings.TrimSpace(c.FormValue("cve_id"))
		pkg := strings.TrimSpace(c.FormValue("package"))
		comment := strings.TrimSpace(c.FormValue("comment"))
		if cveID == "" || comment == "" {
			return fiber.NewError(fiber.StatusBadRequest, "cve_id and comment are required")
		}
		if _, err := repo.UpsertTrivyFindingNote(c.Context(), s, repo.NewTrivyFindingNote{
			BindingID: chrome.Binding.ID, CVEID: cveID, Package: pkg, Status: "false_positive", Comment: comment,
		}); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect(chrome.basePath(), fiber.StatusFound)
	}
}

func deleteTrivyNote() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		id, err := uuid.Parse(c.Params("note_id"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.DeleteTrivyFindingNote(c.Context(), s, id); err != nil {
			return writeRepoErr(c, err)
		}
		return c.Redirect(chrome.basePath(), fiber.StatusFound)
	}
}

func registerTrivyNoteRoutes(app *fiber.App, base string, requireSession, loadPrincipal fiber.Handler) {
	app.Post(base+"/trivy-notes", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), saveTrivyNote())
	app.Post(base+"/trivy-notes/:note_id/delete", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("metric.bind"), deleteTrivyNote())
}
