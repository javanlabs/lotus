package web

import (
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/tenant"
)

// The run page lists a run's items compactly, grouped; an item's details
// (precondition, steps, history, the result form) load into its panel when
// it is opened, so the page never reads every item's history up front.
// Recording a result answers with the refreshed panel plus out-of-band
// swaps of the row's status and the progress header, so a tester moves on
// to the next item without a reload.

// qaStatusTone maps an item or result status to a badge tone.
func qaStatusTone(status string) string {
	switch status {
	case "passed":
		return "pass"
	case "failed", "blocked":
		return "fail"
	}
	return "none"
}

// qaPeopleLabels names every active member of the organization (any
// role), for assignees and executors. Someone who has left shows as "—".
func qaPeopleLabels(c *fiber.Ctx, d Deps, s tenant.Scope) (map[uuid.UUID]string, error) {
	members, err := global.ListMembersForOrg(c.Context(), d.DB, s.OrgID())
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(members))
	for _, m := range members {
		label := m.Name
		if strings.TrimSpace(label) == "" {
			label = m.Email
		}
		out[m.UserID] = label
	}
	return out, nil
}

func personLabel(labels map[uuid.UUID]string, id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	if l, ok := labels[*id]; ok {
		return l
	}
	return "—"
}

// loadQARun reads :rid, which must be a run of the project in the URL.
func loadQARun(c *fiber.Ctx, s tenant.Scope, project *repo.Project) (*repo.QARun, error) {
	id, err := qaOwned(c, s, project, repo.QAKindRun, c.Params("rid"))
	if err != nil {
		return nil, err
	}
	return repo.GetQARun(c.Context(), s, id)
}

// loadQAItem reads :iid, which must be an item of run.
func loadQAItem(c *fiber.Ctx, s tenant.Scope, run *repo.QARun) (*repo.QARunItem, error) {
	id, err := uuid.Parse(c.Params("iid"))
	if err != nil {
		return nil, fiber.ErrNotFound
	}
	item, err := repo.GetQARunItem(c.Context(), s, id)
	if errors.Is(err, repo.ErrNotFound) || (err == nil && item.RunID != run.ID) {
		return nil, fiber.ErrNotFound
	}
	return item, err
}

// qaMaxUploadTotal is what the files of one request may add up to: the
// server takes 25 MB bodies (server.go BodyLimit), less room for the other
// fields. The browser checks it before sending (lotus.js).
const qaMaxUploadTotal = 24 << 20

// qaCollapseAbove: a run or scenario holding more items than this opens
// with its groups closed. Production runs reach about 1,100 items (13 are
// over 500); groups closed turn such a page into a list of groups with
// their progress, and the tester opens the one they work on.
const qaCollapseAbove = 200

func runURL(project *repo.Project, run *repo.QARun) string {
	return qaBase(project) + "/runs/" + run.ID.String()
}

type qaItemRow struct {
	repo.QARunItem
	Key      int
	Changed  bool // the live case is newer than the copy this run holds
	Tone     string
	Assignee string
	Panel    string // where the item panel loads from
}

type qaItemGroup struct {
	Name     string
	Items    []qaItemRow
	Progress repo.QAProgress
}

type qaTesterProgress struct {
	Label    string
	Progress repo.QAProgress
}

type qaRunProgressData struct {
	Total   repo.QAProgress
	Testers []qaTesterProgress
	OOB     bool
}

type qaRunData struct {
	Project    repo.Project
	Base       string
	Run        repo.QARun
	Plan       repo.QAPlan
	PhaseLabel string
	Progress   qaRunProgressData
	Groups     []qaItemGroup
	Shown      int
	Who        string // "", "mine", "unassigned"
	Status     string
	Group      string
	GroupNames []string
	CanExecute bool
	URL        string
	Collapsed  bool // groups start closed: a large run seen unfiltered

	// Managing the run (qa.plan.write): assignment, retest, close.
	CanManage  bool
	People     []httpx.SelectOption
	RetestOf   string
	RetestHref string

	// Save as scenario (qa.case.write).
	CanSaveScenario bool
	PhaseOptions    []httpx.SelectOption
}

// runProgress is the header: overall, then each assignee (unassigned last).
func runProgress(c *fiber.Ctx, s tenant.Scope, run *repo.QARun, labels map[uuid.UUID]string) (qaRunProgressData, error) {
	total, per, err := repo.QARunProgress(c.Context(), s, run.ID)
	if err != nil {
		return qaRunProgressData{}, err
	}
	out := qaRunProgressData{Total: total}
	for id, p := range per {
		if id == uuid.Nil {
			continue
		}
		out.Testers = append(out.Testers, qaTesterProgress{Label: personLabel(labels, &id), Progress: p})
	}
	sort.Slice(out.Testers, func(i, j int) bool {
		return strings.ToLower(out.Testers[i].Label) < strings.ToLower(out.Testers[j].Label)
	})
	if p, ok := per[uuid.Nil]; ok {
		out.Testers = append(out.Testers, qaTesterProgress{Label: "Unassigned", Progress: p})
	}
	return out, nil
}

func qaRunPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		plan, err := repo.GetQAPlan(c.Context(), s, run.PlanID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		labels, err := qaPeopleLabels(c, d, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		me := mustPrincipal(c).UserID
		data := qaRunData{Project: *project, Base: qaBase(project), Run: *run, Plan: *plan, PhaseLabel: qaPhaseLabels[plan.Phase],
			Who: c.Query("who"), Status: c.Query("status"), Group: c.Query("group"),
			CanExecute: mustPrincipal(c).Can("qa.execute") && run.ClosedAt == nil, URL: runURL(project, run)}
		if data.Progress, err = runProgress(c, s, run, labels); err != nil {
			return writeRepoErr(c, err)
		}
		if run.RetestOf != nil {
			if orig, err := repo.GetQARun(c.Context(), s, *run.RetestOf); err == nil {
				data.RetestOf, data.RetestHref = orig.Name, runURL(project, orig)
			}
		}
		if mustPrincipal(c).Can("qa.case.write") {
			data.CanSaveScenario, data.PhaseOptions = true, qaPhaseOptions()
		}
		if mustPrincipal(c).Can("qa.plan.write") {
			data.CanManage = true
			people, err := qaPeople(c, d, s, project)
			if err != nil {
				return writeRepoErr(c, err)
			}
			data.People = []httpx.SelectOption{{Value: "", Label: "Nobody"}}
			for _, p := range people {
				data.People = append(data.People, httpx.SelectOption{Value: p.ID.String(), Label: p.Label})
			}
		}
		items, err := repo.ListQARunItems(c.Context(), s, run.ID, repo.QAItemFilter{})
		if err != nil {
			return writeRepoErr(c, err)
		}
		caseIDs := make([]uuid.UUID, len(items))
		for i, it := range items {
			caseIDs[i] = it.CaseID
		}
		refs, err := repo.QACaseRefs(c.Context(), s, caseIDs)
		if err != nil {
			return writeRepoErr(c, err)
		}
		index := map[string]int{}
		for _, it := range items {
			g, ok := index[it.GroupName]
			if !ok {
				g = len(data.Groups)
				index[it.GroupName] = g
				data.Groups = append(data.Groups, qaItemGroup{Name: it.GroupName})
				data.GroupNames = append(data.GroupNames, it.GroupName)
			}
			data.Groups[g].Progress.Count(it.Status)
			switch {
			case data.Who == "mine" && (it.AssigneeID == nil || *it.AssigneeID != me),
				data.Who == "unassigned" && it.AssigneeID != nil,
				data.Status != "" && it.Status != data.Status,
				data.Group != "" && it.GroupName != data.Group:
				continue
			}
			ref := refs[it.CaseID]
			data.Groups[g].Items = append(data.Groups[g].Items, qaItemRow{
				QARunItem: it, Key: ref.Key, Changed: ref.Version > it.CaseVersion, Tone: qaStatusTone(it.Status),
				Assignee: personLabel(labels, it.AssigneeID), Panel: data.URL + "/items/" + it.ID.String(),
			})
			data.Shown++
		}
		data.Collapsed = len(items) > qaCollapseAbove && data.Who == "" && data.Status == "" && data.Group == ""
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		nav, err := qaNav(c, s, project, httpx.NavCrumb{Label: "Plans", Href: data.Base + "/plans"},
			httpx.NavCrumb{Label: plan.Name, Href: planURL(project, plan)}, httpx.NavCrumb{Label: run.Name})
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return renderPage(c, d, "pages/qa_run.html", httpx.ViewData{
			Title: run.Name + " · " + plan.Name, Org: org, User: user, Nav: nav, Data: data,
		})
	}
}

type qaHistoryEntry struct {
	repo.QAResult
	Tone  string
	By    string
	Files []qaFileLink
}

type qaFileLink struct {
	Name string
	Href string
	Size string
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

type qaItemPanelData struct {
	Item          qaItemRow
	Issues        []qaIssueRow // issues linked to this item
	ReportHref    string       // report an issue from it (qa.issue.report)
	MaxFileBytes  int          // the browser refuses larger files before sending
	MaxTotalBytes int          // … and files adding up to more than one request carries
	Action        string
	History       []qaHistoryEntry
	CanExecute    bool
	Error         string
	Progress      qaRunProgressData
	OOB           bool // also refresh the row status and the progress header
}

func itemPanel(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, run *repo.QARun, item *repo.QARunItem) (qaItemPanelData, error) {
	labels, err := qaPeopleLabels(c, d, s)
	if err != nil {
		return qaItemPanelData{}, err
	}
	refs, err := repo.QACaseRefs(c.Context(), s, []uuid.UUID{item.CaseID})
	if err != nil {
		return qaItemPanelData{}, err
	}
	ref := refs[item.CaseID]
	url := runURL(project, run) + "/items/" + item.ID.String()
	data := qaItemPanelData{
		Item: qaItemRow{QARunItem: *item, Key: ref.Key, Changed: ref.Version > item.CaseVersion, Tone: qaStatusTone(item.Status),
			Assignee: personLabel(labels, item.AssigneeID), Panel: url},
		Action: url + "/result", CanExecute: mustPrincipal(c).Can("qa.execute") && run.ClosedAt == nil,
		MaxFileBytes: repo.MaxQAAttachmentBytes, MaxTotalBytes: qaMaxUploadTotal,
	}
	if mustPrincipal(c).Can("qa.issue.report") {
		data.ReportHref = qaBase(project) + "/issues/new?item=" + item.ID.String()
	}
	linked, err := repo.ListQAIssuesForItem(c.Context(), s, item.ID)
	if err != nil {
		return data, err
	}
	for _, is := range linked {
		data.Issues = append(data.Issues, qaIssueRow{QAIssue: is, Href: fmt.Sprintf("%s/issues/%d", qaBase(project), is.Key),
			SeverityTone: qaSeverityTone(is.Severity)})
	}
	results, err := repo.ListQAResults(c.Context(), s, item.ID)
	if err != nil {
		return data, err
	}
	for _, r := range results {
		id := r.ID
		files, err := repo.ListQAAttachments(c.Context(), s, repo.QAAttachmentOwner{ResultID: &id})
		if err != nil {
			return data, err
		}
		entry := qaHistoryEntry{QAResult: r, Tone: qaStatusTone(r.Status), By: personLabel(labels, r.CreatedBy)}
		for _, f := range files {
			entry.Files = append(entry.Files, qaFileLink{Name: f.FileName, Href: qaBase(project) + "/files/" + f.ID.String(), Size: humanSize(f.SizeBytes)})
		}
		data.History = append(data.History, entry)
	}
	if data.Progress, err = runProgress(c, s, run, labels); err != nil {
		return data, err
	}
	data.Progress.OOB = true
	return data, nil
}

func qaItemPanelUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		item, err := loadQAItem(c, s, run)
		if err != nil {
			return err
		}
		data, err := itemPanel(c, d, s, project, run, item)
		if err != nil {
			return writeRepoErr(c, err)
		}
		c.Type("html")
		return d.Renderer.RenderNamed(c, "qa_item_panel", data)
	}
}

var qaResultChoices = map[string]bool{"passed": true, "failed": true, "blocked": true, "skipped": true}

// recordQAResultUI records one result (and its files) for an item. Every
// file is checked against the size limit before anything is written, so a
// refused upload leaves no result behind.
func recordQAResultUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		run, err := loadQARun(c, s, project)
		if err != nil {
			return err
		}
		item, err := loadQAItem(c, s, run)
		if err != nil {
			return err
		}
		refuse := func(status int, msg string) error {
			if c.Get("HX-Request") == "" {
				return fiber.NewError(status, msg)
			}
			data, err := itemPanel(c, d, s, project, run, item)
			if err != nil {
				return writeRepoErr(c, err)
			}
			data.Error = msg
			c.Type("html")
			return d.Renderer.RenderNamed(c, "qa_item_panel", data)
		}
		status := strings.TrimSpace(c.FormValue("status"))
		if !qaResultChoices[status] {
			return refuse(fiber.StatusUnprocessableEntity, "Choose a result: passed, failed, blocked or skipped.")
		}
		var files []*multipart.FileHeader
		if form, err := c.MultipartForm(); err == nil {
			files = form.File["file"]
		}
		if len(files) > 0 && d.Blob == nil {
			return refuse(fiber.StatusServiceUnavailable, "File storage is not available. Record the result without files, or ask an administrator.")
		}
		for _, f := range files {
			if f.Size > repo.MaxQAAttachmentBytes {
				return refuse(fiber.StatusUnprocessableEntity, "Each file can be at most 20 MB.")
			}
		}
		result, err := repo.RecordQAResult(c.Context(), s, repo.QAResultInput{
			ItemID: item.ID, Status: status, Comment: c.FormValue("comment"), CreatedBy: qaActor(c),
		})
		if errors.Is(err, repo.ErrQAClosed) {
			return refuse(fiber.StatusConflict, "This run is closed.")
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		if err := saveQAFiles(c, d, s, repo.QAAttachmentOwner{ResultID: &result.ID}, files); err != nil {
			if errors.Is(err, repo.ErrQAAttachmentTooLarge) {
				return refuse(fiber.StatusUnprocessableEntity, "Each file can be at most 20 MB.")
			}
			return err
		}
		if c.Get("HX-Request") == "" {
			return c.Redirect(runURL(project, run), fiber.StatusFound)
		}
		item, err = repo.GetQARunItem(c.Context(), s, item.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data, err := itemPanel(c, d, s, project, run, item)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data.OOB = true
		c.Type("html")
		return d.Renderer.RenderNamed(c, "qa_item_panel", data)
	}
}

// qaFileUI serves an attachment of the project in the URL. Images other
// than SVG are shown inline; everything else downloads, typed as bytes,
// with nosniff and a sandbox, so an uploaded HTML or SVG file never runs
// in the app's origin.
func qaFileUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		if d.Blob == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "File storage is not available.")
		}
		id, err := uuid.Parse(c.Params("aid"))
		if err != nil {
			return fiber.ErrNotFound
		}
		pid, err := repo.QAAttachmentProjectID(c.Context(), s, id)
		if errors.Is(err, repo.ErrNotFound) || errors.Is(err, sql.ErrNoRows) || (err == nil && pid != project.ID) {
			return fiber.ErrNotFound
		}
		if err != nil {
			return err
		}
		a, err := repo.GetQAAttachment(c.Context(), s, id)
		if err != nil {
			return writeRepoErr(c, err)
		}
		rc, err := d.Blob.Open(c.Context(), a.StorageKey)
		if errors.Is(err, blob.ErrNotFound) {
			return fiber.ErrNotFound
		}
		if err != nil {
			return err
		}
		inline := strings.HasPrefix(a.ContentType, "image/") && !strings.Contains(a.ContentType, "svg")
		disposition, ctype := "attachment", "application/octet-stream"
		if inline {
			disposition, ctype = "inline", a.ContentType
		}
		c.Set(fiber.HeaderContentType, ctype)
		c.Set(fiber.HeaderContentDisposition, mime.FormatMediaType(disposition, map[string]string{"filename": a.FileName}))
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
		c.Set(fiber.HeaderContentSecurityPolicy, "sandbox")
		return c.SendStream(rc, int(a.SizeBytes))
	}
}
