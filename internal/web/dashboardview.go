package web

import (
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
)

// dashboardRow is one repository (component) in the generalized dashboard
// table (task 4.2). Kept as plain strings/pre-formatted fields rather than
// raw numbers so the template does no arithmetic or formatting itself.
type dashboardRow struct {
	N    int
	Name string
	// Subtext is the small grey line beside the name: the component count
	// on the organization dashboard, and empty on a project page, where
	// naming the project every row already belongs to says nothing.
	Subtext string
	// Href links the row to its component page (task 4.3) — "" only if
	// the caller didn't supply a component id (no current caller does).
	Href  string
	Score string // "84" or "–"
	// ScoreValue is Score as a number for client-side sorting, nil when
	// there is no score — the client then sorts that row last in both
	// directions rather than treating it as zero.
	ScoreValue *int
	ScorePct   string // "84%" for the inline mini-bar width, "0%" if no score
	Tone       string // pass|warn|fail|none
	// Cells is positional: one entry per dashboardViewModel.Columns, in the
	// same order.
	Cells []badgeCell

	// Updated is the component's "last updated" cell (project page only;
	// nil on the organization dashboard, which has no such column).
	Updated *updatedCell

	// TrendURL lazily loads the row's 14-day sparkline (project page only;
	// empty on the organization dashboard, which has no trend column).
	TrendURL string

	// Tags are the project's tags (organization dashboard only), shown
	// beside its name.
	Tags []repo.Tag
}

// dashboardColumn is one integration column in the rows table. Making the
// set data rather than markup is what lets a single dashboard_block render
// both levels: the organization dashboard shows the four collectors that
// actually report, while a project page mirrors the design mockup's fuller
// set including the two that are not collected yet.
type dashboardColumn struct {
	Key   string // matches the KindHeadline key, or "" for a not-yet-collected column
	Label string
}

type badgeCell struct {
	Text string
	Tone string // pass|warn|fail|none
	// Sort is the numeric value this cell sorts by, or nil when the cell
	// has no number (no data, or a status-only kind). Emitted as an empty
	// data attribute so the client sorts those rows last in both
	// directions rather than treating them as zero.
	Sort *int
	// Stale is the hover note of a cell whose integration has not synced
	// for more than a day (drawn dashed), "" otherwise.
	Stale string
	// Title is the hover tooltip. The cell shows the score alone, so the
	// collector's own phrasing ("4 stale branches", "2 critical findings")
	// lives here rather than being lost — it is the reason the number is
	// what it is.
	Title string
	// Href opens the page behind the cell: a component's metric tab for
	// its kind on a project page (metricURL), the project's page for the
	// kind on the organization's project list (projectMetricURL). "" for
	// a cell with nothing to open (see linkCells).
	Href string
	// LinkLabel is the link's accessible name — the cell's text alone
	// ("84") says nothing about where it leads. Set with Href.
	LinkLabel string
}

// kpiCard is one of the four per-kind tabs beside the Lotus score: each
// kind's score averaged over the components that report it — the same
// per-kind average the project score is built from, so a tab and the
// score it feeds always agree.
type kpiCard struct {
	// Key identifies which explanation panel this card's info button
	// opens. Kept separate from Label so the copy can change without
	// silently detaching the button from its panel.
	Key   string
	Label string
	Value string // "89" or "–"
	Unit  string // "/100", "" with no data
	Pct   string // bar width, "89%"
	Tone  string // the band of the score: pass|fail|none
	// Score is Value as a number, nil with no data; its band is the tab's
	// verdict.
	Score *int
	Sub   string
	// Info explains where the number came from, shown in the card's info
	// popover. Kept to a sentence or two: a paragraph in a popover does
	// not get read.
	Info string
	// Href opens the project's page for the kind (projectMetricURL); the
	// whole card is the link. "" draws a card that is not clickable —
	// set on a project page only, where there is one project to open.
	Href string
}

// scoreCard is the "Lotus Score" tile: a project's rolled-up score, its
// band, and how many components the average is hiding.
type scoreCard struct {
	// Info explains the headline number, same role as kpiCard.Info.
	Info string
	// Href opens the project's own page, which the score tab leads back to
	// from a metric page; "" where there is no one project to open.
	Href     string
	HasScore bool
	Score    int
	// Tone and Word are the score's band.
	Tone, Word string
	// Components were rolled up; NeedAttention of them have at least one
	// kind below 80.
	Components, NeedAttention int
}

// updatedCell is a component's "last updated" column: when any of its
// integrations last synced. Which integration is out of date is shown on
// that integration's own cell (badgeCell.Stale), not here.
type updatedCell struct {
	Text  string // "12 min ago", or "–" before any sync
	Title string // the absolute time of Text
	// Sort is the update time in Unix minutes, nil before any sync.
	Sort *int
}

// staleAfter is how old a metric may get before its cell is marked stale:
// every integration is synced many times a day, so a day without one
// means the syncs have been failing, not that nothing changed.
const staleAfter = 24 * time.Hour

// dashboardNow is the clock staleness is measured against; tests pin it.
var dashboardNow = time.Now

// kindSyncedAt is when kind last synced on cm. A kind written before
// per-kind times existed falls back to the row's own updated_at, the
// newest it can be.
func kindSyncedAt(cm repo.ComponentMetric, kh repo.KindHeadline) time.Time {
	if kh.UpdatedAt != nil {
		return *kh.UpdatedAt
	}
	return cm.UpdatedAt
}

// staleNote is the hover text of a cell whose integration has not synced
// for longer than staleAfter, or "" when it is fresh.
func staleNote(kind string, at, now time.Time) string {
	if now.Sub(at) <= staleAfter {
		return ""
	}
	return fmt.Sprintf("Stale: %s has not synced in more than 24 hours.\nLast synced %s (%s).\n\n"+
		"This value may be out of date. Lotus normally syncs every integration several times a day, "+
		"so its recent syncs have most likely failed. Check the metric's sync history, or the connection in Settings → Connections.",
		kindLabels[kind], relativeTimeStrAt(at, now), at.UTC().Format("02 Jan 2006 15:04 MST"))
}

// markStale sets Stale on every cell of cols whose integration on cm has
// not synced for longer than staleAfter.
func markStale(cells []badgeCell, cols []dashboardColumn, cm repo.ComponentMetric, kinds map[string]repo.KindHeadline) {
	now := dashboardNow()
	for i, col := range cols {
		if kh, ok := kinds[col.Key]; ok {
			cells[i].Stale = staleNote(col.Key, kindSyncedAt(cm, kh), now)
		}
	}
}

// updatedCellFor builds the last-updated cell from a component's
// projection row.
func updatedCellFor(cm repo.ComponentMetric, has bool) *updatedCell {
	if !has {
		return &updatedCell{Text: "–", Title: "Not synced yet"}
	}
	sortMin := int(cm.UpdatedAt.Unix() / 60)
	return &updatedCell{
		Text:  relativeTimeStrAt(cm.UpdatedAt, dashboardNow()),
		Title: "Last synced " + cm.UpdatedAt.UTC().Format("02 Jan 2006 15:04 MST"),
		Sort:  &sortMin,
	}
}

type dashboardViewModel struct {
	// TableTitle and RowHeading name what the rows are, because the same
	// dashboard_block component renders both the organization dashboard
	// (rows are projects) and a project's own page (rows are its
	// components).
	TableTitle string
	RowHeading string

	// Columns drives both the header row and each row's Cells.
	Columns []dashboardColumn

	// ShowTrend adds the design's "TREND · 14D" column between SCORE and
	// the integration columns.
	ShowTrend bool

	// FirstMetricCol is the index, among a row's children, of the first
	// metric column — 3 normally, 4 when the trend column is shown. The
	// header uses the same indices, so client-side sorting addresses a
	// column by position instead of needing a parallel registry.
	FirstMetricCol int

	// GridTemplate is the CSS grid definition shared by the header and
	// every row, derived from Columns and ShowTrend so the two can never
	// drift apart.
	//
	// template.CSS, not string: html/template cannot prove an arbitrary
	// string is safe inside a style attribute or a custom property, and
	// substitutes ZgotmplZ instead — which silently collapses the table to
	// a single column. This value is built here from a fixed format and an
	// integer count, never from user input, so declaring it safe is
	// accurate rather than a way around the checker.
	GridTemplate template.CSS

	// ShowUpdated adds the "LAST UPDATED" column after the integration
	// columns (project page only); every row then carries Updated.
	ShowUpdated bool

	// ShowSummary draws the Lotus Score tile and the four KPI cards above
	// the table. Off for the organization dashboard, which leads with the
	// list of projects and their own scores instead — a single number
	// averaged across unrelated projects says little, and the per-project
	// scores in the table below are the ones that carry meaning.
	ShowSummary bool

	Empty        bool // true when there's nothing to show a Repositories table for
	NoProjects   bool // true when the org has zero projects — the "create your first project" empty state
	ProjectCount int
	Score        scoreCard
	KPIs         []kpiCard
	Rows         []dashboardRow
	RowCount     int
	Subtitle     string

	// ProjectList marks the organization's project list: its footer counts
	// the Failing filter too. FailingCount is how many rows fail.
	ProjectList  bool
	FailingCount int
	// Filter is the All / tag chips above the project list.
	Filter tagFilter
}

func toneOf(s collector.Status) string {
	switch s {
	case collector.StatusPass:
		return "pass"
	case collector.StatusFail:
		return "fail"
	default:
		return "none"
	}
}

// badgeFor is one kind's cell for one component: its score, coloured by
// the score's band — never by a status of its own, so a number and its
// colour cannot disagree. With no score the cell is a grey "–" and the
// reason ("No signal", "Not scanned") is its hover text.
func badgeFor(metrics map[string]repo.KindHeadline, kind string) badgeCell {
	kh, ok := metrics[kind]
	if !ok {
		return badgeCell{Text: "–", Tone: "none"}
	}
	if kh.Score != nil {
		return badgeCell{Text: fmt.Sprintf("%d", *kh.Score), Tone: bandTone(kh.Score), Title: headlineText(kh), Sort: kh.Score}
	}
	return badgeCell{Text: "–", Tone: "none", Title: reasonWord(kh)}
}

// headlineText picks the most useful short label out of a KindHeadline:
// a reason with nothing measured, else a count (with its unit
// pluralized), else the score's band word (Healthy / Needs attention),
// else the status in the same words — matching what each collector
// actually populates (see internal/collector/*/result.go). Never a letter
// grade: scores read 0–100 everywhere.
func headlineText(kh repo.KindHeadline) string {
	switch {
	case kh.StateLabel != "":
		return capitalize(kh.StateLabel)
	case kh.CountValue != nil:
		unit := kh.CountLabel
		if *kh.CountValue != 1 && unit != "" {
			unit = pluralize(unit)
		}
		if unit == "" {
			return fmt.Sprintf("%d", *kh.CountValue)
		}
		return fmt.Sprintf("%d %s", *kh.CountValue, unit)
	case kh.Score != nil:
		return scoring.Band(*kh.Score).Word
	case kh.Status != "":
		if w := statusWord(collector.Status(kh.Status)); w != "–" {
			return w
		}
		return strings.ToUpper(kh.Status[:1]) + kh.Status[1:]
	default:
		return "–"
	}
}

// pluralize covers the small, known corpus of CountLabel values every
// collector actually emits ("stale branch", "finding", "issue", "bugs +
// vulnerabilities") — not a general English pluralizer. Skips labels
// already ending in "s" (a compound label like "bugs + vulnerabilities"
// is already plural) and adds "es" rather than "s" after a sibilant
// ending (ch/sh/x/z), matching "branch" -> "branches".
func pluralize(label string) string {
	if strings.HasSuffix(label, "s") {
		return label
	}
	for _, suffix := range []string{"ch", "sh", "x", "z"} {
		if strings.HasSuffix(label, suffix) {
			return label + "es"
		}
	}
	return label + "s"
}

// componentInfo is the join dashboardRows needs between a component's
// identity (name, project) and its live metrics — repo.ComponentMetric
// only carries ids, so the handler builds this from ListComponents +
// ListComponentMetricsForOrg before calling buildDashboardViewModel.
type componentInfo struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	ProjectID   uuid.UUID
	ProjectName string
	ProjectSlug string
}

// url is the component's page.
func (ci componentInfo) url() string {
	return componentURL(&repo.Project{ID: ci.ProjectID, Slug: ci.ProjectSlug}, &repo.Component{ID: ci.ID, Slug: ci.Slug})
}

// metricURL is the component's metric tab for kind.
func (ci componentInfo) metricURL(kind string) string {
	return metricURL(&repo.Project{ID: ci.ProjectID, Slug: ci.ProjectSlug}, &repo.Component{ID: ci.ID, Slug: ci.Slug}, kind)
}

// linkCells points every cell of cols whose kind has something to show
// (has) at that kind's page (href). Any other cell — the kind is not
// bound, or bound but not synced yet — stays plain text, as the health
// map on the dashboard and the unbound tabs of a component's metric strip
// do: an unbound kind's metric URL is a 404, and binding one is the
// component page's Bind metric, which not every reader may use.
func linkCells(cells []badgeCell, cols []dashboardColumn, rowName string, has func(kind string) bool, href func(kind string) string) {
	for i, col := range cols {
		if col.Key == "" || !has(col.Key) {
			continue
		}
		cells[i].Href = href(col.Key)
		cells[i].LinkLabel = fmt.Sprintf("%s, %s: %s", rowName, kindLabels[col.Key], cells[i].Text)
	}
}

// buildDashboardViewModel is pure (no I/O) so it's unit-testable without a
// database: the handler does every repo call, then hands the results here
// to compute scores, KPI rollups, and table rows.
func buildDashboardViewModel(subtitle string, projectCount int, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel {
	if len(components) == 0 {
		return dashboardViewModel{Empty: true, NoProjects: projectCount == 0, ProjectCount: projectCount, Subtitle: subtitle}
	}

	rows := make([]dashboardRow, 0, len(components))

	for _, comp := range components {
		cm, has := metrics[comp.ID]
		var kindMap map[string]repo.KindHeadline
		var score *int
		if has {
			kindMap = cm.Metrics()
			score = cm.Score
		}

		scoreStr, scorePct := "–", "0%"
		if score != nil {
			scoreStr = fmt.Sprintf("%d", *score)
			scorePct = fmt.Sprintf("%d%%", clamp(*score, 0, 100))
		}
		scoreValue := score
		cells := cellsFor(componentColumns, func(k string) badgeCell { return badgeFor(kindMap, k) })
		if has {
			markStale(cells, componentColumns, cm, kindMap)
		}
		// Unbinding drops the kind from the projection row, so a kind
		// present here has a live binding and its metric tab opens.
		linkCells(cells, componentColumns, comp.Name,
			func(k string) bool { _, ok := kindMap[k]; return ok }, comp.metricURL)
		rows = append(rows, dashboardRow{
			Name: comp.Name, Href: comp.url(),
			Score: scoreStr, ScoreValue: scoreValue, ScorePct: scorePct, Tone: bandTone(score),
			Cells:    cells,
			Updated:  updatedCellFor(cm, has),
			TrendURL: trendURLFor(comp),
		})
	}

	// Alphabetical by name: the one order that does not move when the
	// numbers do, so a row stays where the reader last saw it. Sorting by
	// any numeric column is a click away, client-side.
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	for i := range rows {
		rows[i].N = i + 1
	}

	// Every component here is the one project's: the score and the KPI
	// cards are the project's metric tabs, each opening its own page.
	project := &repo.Project{ID: components[0].ProjectID, Slug: components[0].ProjectSlug}
	score, kpis := projectSummary(project, components, metrics)

	vm := dashboardViewModel{
		TableTitle:     "Components",
		RowHeading:     "COMPONENT",
		ShowSummary:    true,
		Columns:        componentColumns,
		ShowTrend:      showTrendColumn,
		FirstMetricCol: firstMetricCol(showTrendColumn),
		GridTemplate:   gridTemplateFor(componentColumns, showTrendColumn) + " minmax(118px,.9fr)",
		ShowUpdated:    true,
		ProjectCount:   projectCount,
		Score:          score,
		KPIs:           kpis,
		Rows:           rows,
		RowCount:       len(rows),
		Subtitle:       subtitle,
	}
	return vm
}

// scoreRank sorts "–" (no score) after every numeric score, ascending —
// matching the mockup's "sorted by score ▲" (worst/most-urgent first).
func scoreRank(score string) int {
	if score == "–" {
		return 1 << 30
	}
	var n int
	_, _ = fmt.Sscanf(score, "%d", &n)
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// buildScoreCard renders the headline tile from a rollup, so "Lotus
// score" means one thing everywhere: its number, its band, and the count
// of components needing attention that the average can hide.
func buildScoreCard(roll repo.ProjectRollup) scoreCard {
	band := scoring.BandOf(roll.Score, "")
	sc := scoreCard{
		Tone: string(band.Tone), Word: band.Word,
		Components: roll.Components, NeedAttention: roll.NeedAttention,
		Info: "Each tool averaged across the components reporting it, then those averages combined — one vote per tool — and rounded once. Unreported tools are skipped, not zeroed. 80 or more is healthy. The line under it counts the components with any tool below 80, which the average can hide.",
	}
	if roll.Score != nil {
		sc.HasScore = true
		sc.Score = *roll.Score
	}
	return sc
}

// attentionText is the count a project's verdict always carries.
func attentionText(need, total int) string {
	return fmt.Sprintf("%d of %d component%s need%s attention", need, total, plural(total, "", "s"), plural(total, "s", ""))
}

// kpiDefs must stay in bindableKinds order — asserted by
// TestKindOrderIsConsistentEverywhere. Labels are kindLabels'.
var kpiDefs = []struct {
	Kind, Info string
}{
	{"sonarqube", "Average SonarQube score of the components reporting it: coverage and the worst rating averaged, times the quality-gate factor."},
	{"trivy", "Average Trivy score: 100 − 20 per critical − 5 per high, capped at 49 with any critical and at 79 when the fail-on policy fails."},
	{"sentry", "Average Sentry health score: unresolved issues weighted by state, volume and recency. Components with no signal are left out."},
	{"gitlab", "Average GitLab score: pipeline success less the stale-branch share × 20, capped at 49 when the last finished pipeline failed."},
}

// projectKPIs is a project's four KPI cards over its components: each
// kind's score averaged over the components reporting it (RollupProject's
// per-kind averages). The project page's card strip and each project-
// level integration page's headline both read it, so the headline cannot
// drift from the card that links to it.
func projectKPIs(components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) []kpiCard {
	var measured []repo.ComponentMetric
	for _, comp := range components {
		if cm, has := metrics[comp.ID]; has {
			measured = append(measured, cm)
		}
	}
	return buildKPIs(repo.RollupProject(measured).Kinds)
}

// projectSummary is a project's metric tabs: the Lotus score and the four
// KPI cards, each linking to its own page. The project page leads with
// them and so does every integration page beneath it, so both read the
// one computation — a tab cannot show one number on the page it opens and
// another on the page it came from.
//
// The score is rolled up exactly as the organization dashboard's row for
// this project is (RollupProject over the components that have a
// projection row), so one project has one Lotus score everywhere.
func projectSummary(project *repo.Project, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) (scoreCard, []kpiCard) {
	var projectMetrics []repo.ComponentMetric
	for _, comp := range components {
		if cm, has := metrics[comp.ID]; has {
			projectMetrics = append(projectMetrics, cm)
		}
	}
	roll := repo.RollupProject(projectMetrics)
	score := buildScoreCard(roll)
	score.Href = projectURL(project)
	kpis := buildKPIs(roll.Kinds)
	for i := range kpis {
		kpis[i].Href = projectMetricURL(project, kpis[i].Key)
	}
	return score, kpis
}

// buildKPIs is one card per kind from its per-kind rollup: the average
// score, its band, and how many of the scored components are below 80.
func buildKPIs(kinds map[string]repo.KindRollup) []kpiCard {
	kpis := make([]kpiCard, 0, len(kpiDefs))
	for _, def := range kpiDefs {
		k := kinds[def.Kind]
		card := kpiCard{Key: def.Kind, Label: strings.ToUpper(kindLabels[def.Kind]), Info: def.Info, Score: k.Score}
		if k.Score == nil {
			card.Value, card.Unit, card.Tone, card.Pct = "–", "", "none", "0%"
			card.Sub = "no data yet"
		} else {
			card.Value, card.Unit = fmt.Sprintf("%d", *k.Score), "/100"
			card.Pct = fmt.Sprintf("%d%%", clamp(*k.Score, 0, 100))
			card.Tone = string(scoring.Band(*k.Score).Tone)
			card.Sub = fmt.Sprintf("%d of %d below %d", k.Below, k.Scored, scoring.PassThreshold)
		}
		kpis = append(kpis, card)
	}
	return kpis
}

// passRateTarget is the share, in percent, at which a share of passing
// components reads as passing: the dashboard's donut and its weakest-
// project insight, which count components rather than average scores.
// It is the same line as a score's: scoring.PassThreshold.
const passRateTarget = scoring.PassThreshold

func rateTone(pct int) string {
	switch {
	case pct >= passRateTarget:
		return "pass"
	default:
		return "fail"
	}
}

// buildProjectDashboardViewModel is the ORGANIZATION dashboard: one row per
// project, each carrying that project's own Lotus score and its tags.
//
// Deliberately separate from buildDashboardViewModel above rather than a
// flag on it. The two answer different questions at different levels of the
// hierarchy — "how are my projects doing" versus "how are this project's
// components doing" — and the project page still needs the per-component
// view, so collapsing them into one parameterised builder would make both
// harder to read for no saving.
//
// Every project given gets a row, even one with no components yet, in one
// flat list sorted by name: tags group projects now, not a folder tree.
func buildProjectDashboardViewModel(subtitle string, projects []repo.Project, tags map[uuid.UUID][]repo.Tag, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel {
	componentCount := make(map[uuid.UUID]int, len(projects))
	projectMetrics := make(map[uuid.UUID][]repo.ComponentMetric, len(projects))
	var allMetrics []repo.ComponentMetric

	for _, comp := range components {
		componentCount[comp.ProjectID]++
		cm, has := metrics[comp.ID]
		if !has {
			continue
		}
		projectMetrics[comp.ProjectID] = append(projectMetrics[comp.ProjectID], cm)
		allMetrics = append(allMetrics, cm)
	}

	rows := make([]dashboardRow, 0, len(projects))
	failing := 0 // projects whose score is in the needs-attention band
	for _, p := range projects {
		roll := repo.RollupProject(projectMetrics[p.ID])
		row := projectRow(p, componentCount[p.ID], roll)
		row.Tags = tags[p.ID]
		if row.Tone == "fail" {
			failing++
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	for i := range rows {
		rows[i].N = i + 1
	}

	// The fleet's summary is not drawn on this page (ShowSummary is off),
	// but it is the same per-kind rollup, over every component.
	fleet := repo.RollupProject(allMetrics)
	return dashboardViewModel{
		Empty:          len(projects) == 0,
		NoProjects:     len(projects) == 0,
		TableTitle:     "Projects",
		RowHeading:     "PROJECT",
		Columns:        projectColumns,
		FirstMetricCol: firstMetricCol(false),
		GridTemplate:   gridTemplateFor(projectColumns, false),
		ProjectList:    true,
		FailingCount:   failing,
		ProjectCount:   len(projects),
		Score:          buildScoreCard(fleet),
		KPIs:           buildKPIs(fleet.Kinds),
		Rows:           rows,
		RowCount:       len(rows),
		Subtitle:       subtitle,
	}
}

// projectRow is one project's row in the organization's project list.
func projectRow(p repo.Project, components int, roll repo.ProjectRollup) dashboardRow {
	scoreStr, scorePct := "–", "0%"
	if roll.Score != nil {
		scoreStr = fmt.Sprintf("%d", *roll.Score)
		scorePct = fmt.Sprintf("%d%%", clamp(*roll.Score, 0, 100))
	}
	cells := cellsFor(projectColumns, func(k string) badgeCell { return projectBadge(roll, k) })
	// A rollup cell opens the project's page for its kind, as the project
	// page's KPI tab for that kind does; a kind no component reports stays
	// plain, as projectBadge draws it.
	linkCells(cells, projectColumns, p.Name,
		func(k string) bool { return roll.Kinds[k].Count > 0 },
		func(k string) string { return projectMetricURL(&p, k) })
	// The row's verdict is its score's band, so it carries the count the
	// average can hide: how many of its measured components have a kind
	// below 80.
	subtext := fmt.Sprintf("%d component%s", components, plural(components, "", "s"))
	if roll.Components > 0 {
		subtext = attentionText(roll.NeedAttention, roll.Components)
	}
	return dashboardRow{
		Name:       p.Name,
		Subtext:    subtext,
		Href:       projectURL(&p),
		Score:      scoreStr,
		ScoreValue: roll.Score,
		ScorePct:   scorePct,
		Tone:       bandTone(roll.Score),
		Cells:      cells,
	}
}

// projectBadge renders one integration's project-wide rollup for the table.
// Distinct from badgeFor, which shows a single component's headline text:
// at project level there is no single headline, so the cell reports the
// averaged score alone. It used to append "· N" for the number of
// components reporting — dropped, since that count is available on the
// project page and here it only made every cell read like a fraction.
// A kind reported but scored nowhere (every image not scanned, say) is a
// grey "–", never a coloured word.
func projectBadge(roll repo.ProjectRollup, kind string) badgeCell {
	k, ok := roll.Kinds[kind]
	if !ok || k.Count == 0 {
		return badgeCell{Text: "–", Tone: "none"}
	}
	if k.Score == nil {
		return badgeCell{Text: "–", Tone: "none", Title: fmt.Sprintf("no score on any of %d component%s", k.Count, plural(k.Count, "", "s"))}
	}
	title := fmt.Sprintf("averaged over %d component%s · %d below %d", k.Scored, plural(k.Scored, "", "s"), k.Below, scoring.PassThreshold)
	return badgeCell{Text: fmt.Sprintf("%d", *k.Score), Tone: bandTone(k.Score), Title: title, Sort: k.Score}
}

func statusWord(s collector.Status) string {
	switch s {
	case collector.StatusPass:
		return scoring.WordHealthy
	case collector.StatusFail:
		return scoring.WordNeedsAttention
	default:
		return "–"
	}
}

// showTrendColumn parks the "TREND · 14D" sparkline column. One constant
// drives both the header cell and the per-row cell: gated separately, a
// half-enabled trend puts one more cell in every row than the header has
// and silently shears the whole grid. The sparkline endpoint and its
// chart builder stay in place, so turning this back on is this one line.
const showTrendColumn = false

// projectColumns is what the organization dashboard shows: the four
// collectors that actually report data today.
var projectColumns = columnsFor(bindableKinds)

// componentColumns is a project page's integration columns: the same four
// as the organization dashboard. The design's PERF and SECURITY columns
// were dropped (2026-10-04): nothing collects them, and two columns that
// only ever said "no data" pushed the last-updated column off screen.
var componentColumns = columnsFor(bindableKinds)

// columnHeadings are the short, all-caps forms used in table headers —
// deliberately terser than bindview.go's kindLabels, which are the
// sentence-case names shown in the wizard.
var columnHeadings = map[string]string{
	"sonarqube": "SONAR", "trivy": "TRIVY", "sentry": "SENTRY", "gitlab": "GIT",
}

// columnsFor turns the canonical kind order into table columns, so the
// table can never disagree with the wizard about which integration comes
// third. See AGENTS.md.
func columnsFor(kinds []string) []dashboardColumn {
	cols := make([]dashboardColumn, 0, len(kinds))
	for _, k := range kinds {
		cols = append(cols, dashboardColumn{Key: k, Label: columnHeadings[k]})
	}
	return cols
}

func cellsFor(cols []dashboardColumn, lookup func(kind string) badgeCell) []badgeCell {
	out := make([]badgeCell, 0, len(cols))
	for _, col := range cols {
		if col.Key == "" {
			out = append(out, badgeCell{Text: "–", Tone: "none"})
			continue
		}
		out = append(out, lookup(col.Key))
	}
	return out
}

// gridTemplateFor keeps the header row and the data rows on one definition,
// so adding a column cannot leave the two misaligned. Widths follow the
// mockup: a narrow index, a wide name, a fixed score, an optional flexible
// trend, then evenly-sized integration columns.
func gridTemplateFor(cols []dashboardColumn, trend bool) template.CSS {
	base := "26px minmax(190px,1.5fr) 84px"
	if trend {
		base += " 1fr"
	}
	return template.CSS(fmt.Sprintf("%s repeat(%d,minmax(78px,.72fr))", base, len(cols)))
}

func trendURLFor(comp componentInfo) string {
	if !showTrendColumn {
		return ""
	}
	return comp.url() + "/sparkline"
}

// firstMetricCol mirrors gridTemplateFor's leading-column count: #, name,
// score, and optionally trend.
func firstMetricCol(trend bool) int {
	if trend {
		return 4
	}
	return 3
}

// bandTone is a score's colour, pass|fail|none: the one place a number
// becomes a colour (scoring.BandOf), so no cell can colour a score by a
// rule of its own.
func bandTone(score *int) string { return string(scoring.BandOf(score, "").Tone) }

// reasonWord says why a kind has no score: the collector's own reason
// ("No signal", "Not scanned", "No pipeline data"), else "No data".
// Headlines stored before the reason words were capitalised read the
// same.
func reasonWord(kh repo.KindHeadline) string {
	if kh.StateLabel != "" {
		return capitalize(kh.StateLabel)
	}
	return scoring.ReasonNoData
}
