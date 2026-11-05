package web

import (
	"html/template"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// A project's integration page (/projects/{p}/metrics/{kind}) is one
// integration across every component of the project — the project-scoped
// descendant of the legacy fleet pages (/sonar, /gitlab, /trivy, /sentry).
// A component has one live binding per kind (migration 00035), so each row
// is one component's binding and opens that binding's own detail page.
//
// The page leads with the same metric tabs as the project page (Score and
// KPIs, from projectSummary), the kind's own tab active: the tab is the
// headline, and it must show the number the project page's card does,
// since that card is what links here. The kind's other numbers (Stats) sit
// beneath it as the tab's panel.

// kindPage is everything the page template draws. The four kinds differ
// only in their summary stats and their columns; Columns and each row's
// Cells are positional, built together per kind, so the header and the
// rows cannot disagree.
type kindPage struct {
	Kind, KindLabel string
	Project         repo.Project
	ProjectURL      string
	// ComponentCount is every component of the project, bound or not.
	ComponentCount int

	// Score and KPIs are the project's metric tabs; Kind is the active one.
	Score scoreCard
	KPIs  []kpiCard
	// Stats are the kind's other important numbers, drawn under the tabs.
	Stats []kindStat

	Columns []kindColumn
	Rows    []kindRow
	// GridTemplate and MinWidth are the shared grid of the header and the
	// rows: index, component, the kind's columns, last synced. template.CSS
	// for the reason dashboardViewModel.GridTemplate gives.
	GridTemplate template.CSS
	MinWidth     int
	// SyncedCol is the last-synced column's index among a row's children.
	SyncedCol int

	// Unbound is the project's components with no binding of the kind —
	// listed under the table rather than as rows: a row would be all "–".
	Unbound []kindLink
}

type kindStat struct{ Label, Value, Tone, Sub string }

type kindLink struct{ Name, Href string }

// kindColumn is one of a kind's columns. Sort is the lotusSort kind ("num"
// or "text"), "" for a column that does not sort. Col is the column's
// index among a row's children, which is how lotusSort addresses it.
type kindColumn struct {
	Label, Title string
	Width        string // grid track
	Sort         string
	Right        bool
	Col          int
}

// kindRow is one component's binding of the kind.
type kindRow struct {
	Name        string
	Href        string // the binding's detail page
	ExternalKey string
	// ProviderURL opens the binding in the integration itself (new tab).
	ProviderURL string
	Tone        string // the kind's headline status on this component
	Cells       []kindCell
	Synced      *updatedCell
}

// kindCell is one cell of a kind's column.
//
// Sort is the value lotusSort orders by, "" when the cell has none (it
// then sorts last both ways). lotusSort reads ascending as "worst first",
// so a count where fewer is better (bugs, findings, issues) carries its
// negation: the largest count comes first.
type kindCell struct {
	Type  string // "text" (default), "badge", "grades", "governance"
	Text  string
	Sub   string // a second, muted line
	Title string
	Href  string // the cell's own link out (new tab)
	Sort  string
	Fail  bool // drawn in the fail colour: the value is over its threshold
	Mono  bool
	// Badge is a "badge" cell's pill; Stale on it draws the dashed
	// treatment of an integration that has not synced for a day.
	Badge badgeCell
	// Grades is a "grades" cell's letters (SonarQube's R·S·M).
	Grades []badgeCell
	// Governance is a "governance" cell (GitLab).
	Protected, Archived bool
	Visibility          string
}

const kindMissing = "–"

// sortInt and sortFloat are a cell's Sort value; negInt is a count where
// fewer is better.
func sortInt(v int) string       { return strconv.Itoa(v) }
func negInt(v int) string        { return strconv.Itoa(-v) }
func sortFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func sortTime(t time.Time) string {
	return strconv.FormatInt(t.Unix()/60, 10)
}

// thousands writes n with comma separators, as the legacy fleet pages did.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// timeCell is a relative time with its absolute form on hover; t nil is
// "–".
func timeCell(t *time.Time) kindCell {
	if t == nil {
		return kindCell{Text: kindMissing}
	}
	return kindCell{Text: relativeTimeStrAt(*t, dashboardNow()), Title: absTime(*t), Sort: sortTime(*t)}
}

// countCell is a count where fewer is better; fail marks it over its
// threshold.
func countCell(v *int, fail bool) kindCell {
	if v == nil {
		return kindCell{Text: kindMissing}
	}
	return kindCell{Text: thousands(int64(*v)), Sort: negInt(*v), Fail: fail}
}

// syncedCell is a binding's last-synced cell.
func syncedCell(at time.Time) *updatedCell {
	m := int(at.Unix() / 60)
	return &updatedCell{Text: relativeTimeStrAt(at, dashboardNow()), Title: "Last synced " + absTime(at), Sort: &m}
}

// kindPageBase builds what every kind's page shares: the metric tabs, the
// rows' identity, tone and last-synced cell (from the component's
// projection row, as the project page's own cells are), and the unbound
// components. bindings comes from repo.ListProjectBindingsByKind.
type kindPageBase struct {
	page    kindPage
	project *repo.Project
	// rows and bindings are parallel: rows[i] is bindings[i]'s.
	bindings []repo.ProjectBinding
	// stale is each row's stale note, applied to its status cell once the
	// kind has built it.
	stale []string
}

func newKindPage(project *repo.Project, kind string, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric, bindings []repo.ProjectBinding) *kindPageBase {
	b := &kindPageBase{project: project, page: kindPage{
		Kind: kind, KindLabel: kindLabels[kind], Project: *project, ProjectURL: projectURL(project),
		ComponentCount: len(components),
	}}
	b.page.Score, b.page.KPIs = projectSummary(project, components, metrics)

	byID := make(map[uuid.UUID]componentInfo, len(components))
	for _, c := range components {
		byID[c.ID] = c
	}
	bound := map[uuid.UUID]bool{}
	now := dashboardNow()
	for _, bd := range bindings {
		comp, ok := byID[bd.ComponentID]
		if !ok {
			continue
		}
		bound[comp.ID] = true
		row := kindRow{
			Name: comp.Name, ExternalKey: bd.ExternalKey, Tone: "none",
			Href: metricURL(project, &repo.Component{ID: comp.ID, Slug: comp.Slug}, kind),
		}
		stale := ""
		if cm, has := metrics[comp.ID]; has {
			if kh, ok := cm.Metrics()[kind]; ok {
				row.Tone = toneOf(collector.Status(kh.Status))
				at := kindSyncedAt(cm, kh)
				row.Synced = syncedCell(at)
				stale = staleNote(kind, at, now)
			}
		}
		b.page.Rows = append(b.page.Rows, row)
		b.bindings = append(b.bindings, bd)
		b.stale = append(b.stale, stale)
	}
	for _, c := range components {
		if !bound[c.ID] {
			b.page.Unbound = append(b.page.Unbound, kindLink{Name: c.Name, Href: c.url()})
		}
	}
	sort.SliceStable(b.page.Unbound, func(i, j int) bool {
		return strings.ToLower(b.page.Unbound[i].Name) < strings.ToLower(b.page.Unbound[j].Name)
	})
	return b
}

// Grid geometry shared with .lotus-pm-grid and .lotus-pm-row (app.css):
// the column gap and the row's horizontal padding, both sides.
const (
	kindGridGap     = 8
	kindGridPadding = 28
)

// setColumns fixes the kind's columns, numbering them after the index and
// component columns and deriving the grid. The tracks' minimums are sized
// so that every kind fits a 1280px screen beside the sidebar; MinWidth is
// their sum, below which the table scrolls inside its card.
func (b *kindPageBase) setColumns(cols []kindColumn) {
	tracks := []string{"26px", "minmax(140px,1.6fr)"}
	for i := range cols {
		cols[i].Col = i + 2
		tracks = append(tracks, cols[i].Width)
	}
	tracks = append(tracks, "minmax(80px,.7fr)")
	width := kindGridPadding + kindGridGap*(len(tracks)-1)
	for _, t := range tracks {
		width += trackMin(t)
	}
	b.page.Columns = cols
	b.page.SyncedCol = len(cols) + 2
	b.page.GridTemplate = template.CSS(strings.Join(tracks, " ")) //nolint:gosec // built from fixed widths, never user input
	b.page.MinWidth = width
}

// trackMin is a grid track's minimum width in px: "72px" or
// "minmax(72px,1fr)".
func trackMin(track string) int {
	t := strings.TrimPrefix(track, "minmax(")
	t, _, _ = strings.Cut(t, ",")
	n, _ := strconv.Atoi(strings.TrimSuffix(t, "px"))
	return n
}

// finish applies each row's stale note to its status cell (statusCol, an
// index into Cells), falls back to the snapshot's own sync time where the
// projection has none, and sorts the rows by component name — the order
// that does not move when the numbers do.
func (b *kindPageBase) finish(statusCol int, syncedAt func(i int) *time.Time) kindPage {
	for i := range b.page.Rows {
		r := &b.page.Rows[i]
		if statusCol >= 0 && statusCol < len(r.Cells) {
			r.Cells[statusCol].Badge.Stale = b.stale[i]
		}
		if r.Synced == nil {
			if at := syncedAt(i); at != nil {
				r.Synced = syncedCell(*at)
			} else {
				r.Synced = &updatedCell{Text: kindMissing, Title: "Not synced yet"}
			}
		}
	}
	sort.SliceStable(b.page.Rows, func(i, j int) bool {
		return strings.ToLower(b.page.Rows[i].Name) < strings.ToLower(b.page.Rows[j].Name)
	})
	return b.page
}

func projectMetricPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		project, err := resolveProject(c)
		if err != nil {
			return err
		}
		kind := c.Params("kind")
		if !slices.Contains(bindableKinds, kind) {
			return fiber.ErrNotFound
		}
		org, user, err := shellChrome(c, d.DB)
		if err != nil {
			return err
		}
		projects, err := repo.ListProjects(c.Context(), s, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		comps, err := repo.ListComponents(c.Context(), s, project.ID, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		components := make([]componentInfo, 0, len(comps))
		for _, comp := range comps {
			components = append(components, componentInfo{ID: comp.ID, Name: comp.Name, Slug: comp.Slug,
				ProjectID: project.ID, ProjectName: project.Name, ProjectSlug: project.Slug})
		}
		metricRows, err := repo.ListComponentMetricsForProject(c.Context(), s, project.ID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		metrics := make(map[uuid.UUID]repo.ComponentMetric, len(metricRows))
		for _, m := range metricRows {
			metrics[m.ComponentID] = m
		}
		bindings, err := repo.ListProjectBindingsByKind(c.Context(), s, project.ID, kind)
		if err != nil {
			return writeRepoErr(c, err)
		}
		base := newKindPage(project, kind, components, metrics, bindings)

		var page kindPage
		switch kind {
		case "sonarqube":
			snaps, err := repo.ListLatestSonarSnapshotsForProject(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			page = buildSonarKindPage(base, snaps)
		case "trivy":
			snaps, err := repo.ListLatestTrivySnapshotsForProject(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			fps, err := repo.CountFalsePositivesForProject(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			page = buildTrivyKindPage(base, snaps, fps)
		case "sentry":
			envs, err := repo.ListLatestSentryEnvironmentsForProject(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			page = buildSentryKindPage(base, envs, metrics)
		case "gitlab":
			snaps, err := repo.ListLatestGitlabSnapshotsForProject(c.Context(), s, project.ID)
			if err != nil {
				return writeRepoErr(c, err)
			}
			page = buildGitlabKindPage(base, snaps)
		}

		menus, err := loadTrailMenus(c.Context(), s, projects, project, nil, kind)
		if err != nil {
			return writeRepoErr(c, err)
		}
		trail := projectKindTrail(project, kind, menus)
		c.Type("html")
		return renderPage(c, d, "pages/project_metric.html", httpx.ViewData{
			Title: projectTitle(project, kindLabels[kind]), Org: org, User: user,
			Nav:  navWithTrail("projects", len(projects), trail),
			Data: page,
		})
	}
}

func registerProjectMetricRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/projects/:id/metrics/:kind", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), canonicalURL(), projectMetricPage(d))
}
