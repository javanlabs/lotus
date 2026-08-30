package web

import (
	"fmt"
	"html/template"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
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

	// TrendURL lazily loads the row's 14-day sparkline (project page only;
	// empty on the organization dashboard, which has no trend column).
	TrendURL string
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
	// Title is the hover tooltip. The cell shows the score alone, so the
	// collector's own phrasing ("4 stale branches", "2 critical findings")
	// lives here rather than being lost — it is the reason the number is
	// what it is.
	Title string
}

// kpiCard is one of the four fleet-wide rollup cards (Sonar gate, Trivy
// clean, Sentry health, Git hygiene in the mockup) — one per collector
// kind, showing the pass rate across every component that has that kind
// bound.
type kpiCard struct {
	// Key identifies which explanation panel this card's info button
	// opens. Kept separate from Label so the copy can change without
	// silently detaching the button from its panel.
	Key   string
	Label string
	Value string // "89" or "–"
	Unit  string // "%"
	Pct   string // bar width, "89%"
	Tone  string
	Sub   string
	// Info explains where the number came from, shown in the card's info
	// popover. These cards are pass RATES, not averaged scores — the
	// distinction nothing on the card itself conveys, and the most common
	// way to misread the strip. Kept to a sentence or two: a paragraph in
	// a popover does not get read.
	Info string
}

// scoreCard is the large "Lotus Score" tile: the org-wide rollup score and
// status distribution across every component with a projection row.
type scoreCard struct {
	// Info explains the headline number, same role as kpiCard.Info.
	Info                   string
	HasScore               bool
	Score                  int
	HealthyPct, FailingPct int // segment widths, sum to 100 (0 each if NoData)
	HealthyN, FailingN     int
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

func badgeFor(metrics map[string]repo.KindHeadline, kind string) badgeCell {
	kh, ok := metrics[kind]
	if !ok {
		return badgeCell{Text: "–", Tone: "none"}
	}
	tone := toneOf(collector.Status(kh.Status))
	if kh.Score != nil {
		return badgeCell{Text: fmt.Sprintf("%d", *kh.Score), Tone: tone, Title: headlineText(kh), Sort: kh.Score}
	}
	// No score to show — a kind that only grades or counts still needs a
	// readable cell, so fall back to the phrasing itself.
	return badgeCell{Text: headlineText(kh), Tone: tone}
}

// headlineText picks the most useful short label out of a KindHeadline:
// a count (with its unit pluralized) beats a letter grade beats the bare
// status word — matching what each collector actually populates (see
// internal/collector/*/result.go).
func headlineText(kh repo.KindHeadline) string {
	switch {
	case kh.CountValue != nil:
		unit := kh.CountLabel
		if *kh.CountValue != 1 && unit != "" {
			unit = pluralize(unit)
		}
		if unit == "" {
			return fmt.Sprintf("%d", *kh.CountValue)
		}
		return fmt.Sprintf("%d %s", *kh.CountValue, unit)
	case kh.Grade != "":
		return kh.Grade
	case kh.Status != "":
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
	ProjectID   uuid.UUID
	ProjectName string
}

// buildDashboardViewModel is pure (no I/O) so it's unit-testable without a
// database: the handler does every repo call, then hands the results here
// to compute scores, KPI rollups, and table rows.
func buildDashboardViewModel(subtitle string, projectCount int, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel {
	if len(components) == 0 {
		return dashboardViewModel{Empty: true, NoProjects: projectCount == 0, ProjectCount: projectCount, Subtitle: subtitle}
	}

	rows := make([]dashboardRow, 0, len(components))
	var statuses []collector.Status
	// projectMetrics is the same set the rows are built from, handed to
	// RollupProject so this page's headline score is computed identically
	// to the one the organization dashboard shows for this project.
	var projectMetrics []repo.ComponentMetric
	kindStatuses := map[string][]collector.Status{"gitlab": nil, "sonarqube": nil, "trivy": nil, "sentry": nil}
	kindMeasured := map[string]int{}

	for _, comp := range components {
		cm, has := metrics[comp.ID]
		var kindMap map[string]repo.KindHeadline
		status := collector.StatusUnknown
		var score *int
		if has {
			kindMap = cm.Metrics()
			status = cm.StatusValue()
			score = cm.Score
		}
		statuses = append(statuses, status)
		if has {
			projectMetrics = append(projectMetrics, cm)
		}

		for kind := range kindStatuses {
			if kh, ok := kindMap[kind]; ok {
				kindStatuses[kind] = append(kindStatuses[kind], collector.Status(kh.Status))
				kindMeasured[kind]++
			}
		}

		scoreStr, scorePct := "–", "0%"
		if score != nil {
			scoreStr = fmt.Sprintf("%d", *score)
			scorePct = fmt.Sprintf("%d%%", clamp(*score, 0, 100))
		}
		scoreValue := score
		rows = append(rows, dashboardRow{
			Name: comp.Name, Href: "/components/" + comp.ID.String(),
			Score: scoreStr, ScoreValue: scoreValue, ScorePct: scorePct, Tone: toneOf(status),
			Cells:    cellsFor(componentColumns, func(k string) badgeCell { return badgeFor(kindMap, k) }),
			TrendURL: trendURLFor(comp.ID),
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

	vm := dashboardViewModel{
		TableTitle:     "Components",
		RowHeading:     "COMPONENT",
		ShowSummary:    true,
		Columns:        componentColumns,
		ShowTrend:      showTrendColumn,
		FirstMetricCol: firstMetricCol(showTrendColumn),
		GridTemplate:   gridTemplateFor(componentColumns, showTrendColumn),
		ProjectCount:   projectCount,
		Score:          buildScoreCard(statuses, repo.RollupProject(projectMetrics).Score),
		KPIs:           buildKPIs(kindStatuses, kindMeasured, len(components)),
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

// buildScoreCard renders the headline tile. score is the number to show —
// callers pass the rollup appropriate to their level, so "Lotus score"
// means one thing everywhere. statuses drive only the healthy/at-risk/
// failing distribution beneath it, which is a per-component count and
// deliberately independent of how the score itself is computed.
func buildScoreCard(statuses []collector.Status, score *int) scoreCard {
	healthy, failing := 0, 0
	for _, s := range statuses {
		switch s {
		case collector.StatusPass:
			healthy++
		case collector.StatusFail:
			failing++
		}
	}
	total := healthy + failing
	sc := scoreCard{
		HealthyN: healthy, FailingN: failing,
		Info: "Each tool averaged across the components reporting it, then those averages combined — one vote per tool. Unreported tools are skipped, not zeroed. The bar splits components by status.",
	}
	if score != nil {
		sc.HasScore = true
		sc.Score = *score
	}
	if total > 0 {
		sc.HealthyPct = healthy * 100 / total
		sc.FailingPct = 100 - sc.HealthyPct
	}
	return sc
}

// kpiDefs must stay in bindableKinds order — asserted by
// TestKindOrderIsConsistentEverywhere.
var kpiDefs = []struct {
	Kind, Label, Info string
}{
	{"sonarqube", "SONAR GATE",
		"Share of components whose quality gate passes. Components without SonarQube are excluded, not failed."},
	{"trivy", "TRIVY CLEAN",
		"Share of components with no critical and no high findings. Medium and low don't count against it."},
	{"sentry", "SENTRY HEALTH",
		"Share of components Sentry grades A or B (score 80+). C, D and E don't count."},
	{"gitlab", "GIT HYGIENE",
		"Share of components whose last pipeline passed. Still-running counts as at risk."},
}

func buildKPIs(kindStatuses map[string][]collector.Status, measured map[string]int, totalComponents int) []kpiCard {
	kpis := make([]kpiCard, 0, len(kpiDefs))
	for _, def := range kpiDefs {
		rate, ok := repo.PassRate(kindStatuses[def.Kind])
		card := kpiCard{Key: def.Kind, Label: def.Label, Info: def.Info}
		if !ok {
			card.Value, card.Unit, card.Tone, card.Pct = "–", "", "none", "0%"
			card.Sub = "no data yet"
		} else {
			pct := int(rate*100 + 0.5)
			card.Value, card.Unit = fmt.Sprintf("%d", pct), "%"
			card.Pct = fmt.Sprintf("%d%%", pct)
			card.Tone = rateTone(pct)
			card.Sub = fmt.Sprintf("%d/%d components reporting", measured[def.Kind], totalComponents)
		}
		kpis = append(kpis, card)
	}
	return kpis
}

func rateTone(pct int) string {
	switch {
	case pct >= 80:
		return "pass"
	default:
		return "fail"
	}
}

// buildProjectDashboardViewModel is the ORGANIZATION dashboard: one row per
// project, each carrying that project's own Lotus score.
//
// Deliberately separate from buildDashboardViewModel above rather than a
// flag on it. The two answer different questions at different levels of the
// hierarchy — "how are my projects doing" versus "how are this project's
// components doing" — and the project page still needs the per-component
// view, so collapsing them into one parameterised builder would make both
// harder to read for no saving.
func buildProjectDashboardViewModel(subtitle string, projectCount int, components []componentInfo, metrics map[uuid.UUID]repo.ComponentMetric) dashboardViewModel {
	if len(components) == 0 {
		return dashboardViewModel{Empty: true, NoProjects: projectCount == 0, ProjectCount: projectCount, Subtitle: subtitle}
	}

	type projectAcc struct {
		id      uuid.UUID
		name    string
		total   int
		metrics []repo.ComponentMetric
	}
	order := make([]uuid.UUID, 0, len(components))
	accs := make(map[uuid.UUID]*projectAcc, len(components))

	kindStatuses := map[string][]collector.Status{"gitlab": nil, "sonarqube": nil, "trivy": nil, "sentry": nil}
	kindMeasured := map[string]int{}

	for _, comp := range components {
		acc, seen := accs[comp.ProjectID]
		if !seen {
			acc = &projectAcc{id: comp.ProjectID, name: comp.ProjectName}
			accs[comp.ProjectID] = acc
			order = append(order, comp.ProjectID)
		}
		acc.total++

		cm, has := metrics[comp.ID]
		if !has {
			continue
		}
		acc.metrics = append(acc.metrics, cm)

		// The KPI cards stay component-scoped: they answer "how much of the
		// fleet reports this integration at all", which is a coverage
		// question about components, not about projects.
		for kind := range kindStatuses {
			if kh, ok := cm.Metrics()[kind]; ok {
				kindStatuses[kind] = append(kindStatuses[kind], collector.Status(kh.Status))
				kindMeasured[kind]++
			}
		}
	}

	rows := make([]dashboardRow, 0, len(order))
	var statuses []collector.Status
	var scores []*int

	for _, id := range order {
		acc := accs[id]
		roll := repo.RollupProject(acc.metrics)
		statuses = append(statuses, roll.Status)
		scores = append(scores, roll.Score)

		scoreStr, scorePct := "–", "0%"
		if roll.Score != nil {
			scoreStr = fmt.Sprintf("%d", *roll.Score)
			scorePct = fmt.Sprintf("%d%%", clamp(*roll.Score, 0, 100))
		}
		rows = append(rows, dashboardRow{
			Name:    acc.name,
			Subtext: fmt.Sprintf("%d component%s", acc.total, plural(acc.total, "", "s")),
			Href:    "/projects/" + acc.id.String(),
			Score:   scoreStr, ScorePct: scorePct, Tone: toneOf(roll.Status),
			Cells: cellsFor(projectColumns, func(k string) badgeCell { return projectBadge(roll, k) }),
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

	return dashboardViewModel{
		TableTitle:     "Projects",
		RowHeading:     "PROJECT",
		Columns:        projectColumns,
		FirstMetricCol: firstMetricCol(false),
		GridTemplate:   gridTemplateFor(projectColumns, false),
		ProjectCount:   projectCount,
		Score:          buildScoreCard(statuses, repo.RollupScore(scores)),
		KPIs:           buildKPIs(kindStatuses, kindMeasured, len(components)),
		Rows:           rows,
		RowCount:       len(rows),
		Subtitle:       subtitle,
	}
}

// projectBadge renders one integration's project-wide rollup for the table.
// Distinct from badgeFor, which shows a single component's headline text:
// at project level there is no single headline, so the cell reports the
// averaged score alone. It used to append "· N" for the number of
// components reporting — dropped, since that count is available on the
// project page and here it only made every cell read like a fraction.
// A kind with status but no score (Trivy counts rather than grades at
// component level, but its project rollup still averages the per-image
// scores) falls back to a bare status word so the tone is never shown on
// an empty cell.
func projectBadge(roll repo.ProjectRollup, kind string) badgeCell {
	k, ok := roll.Kinds[kind]
	if !ok || k.Count == 0 {
		return badgeCell{Text: "–", Tone: "none"}
	}
	title := fmt.Sprintf("averaged over %d component%s", k.Count, plural(k.Count, "", "s"))
	if k.Score != nil {
		return badgeCell{Text: fmt.Sprintf("%d", *k.Score), Tone: toneOf(k.Status), Title: title, Sort: k.Score}
	}
	return badgeCell{Text: statusWord(k.Status), Tone: toneOf(k.Status), Title: title}
}

func statusWord(s collector.Status) string {
	switch s {
	case collector.StatusPass:
		return "Pass"
	case collector.StatusFail:
		return "Fail"
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

// componentColumns mirrors the Claude Design mockup's repositories table.
// PERF and SECURITY carry an empty Key deliberately: the design anticipates
// them (the sidebar already lists both as forthcoming) but nothing collects
// them yet, so they render as "no data" rather than as a passing or failing
// verdict nobody measured.
var componentColumns = append(columnsFor(bindableKinds),
	dashboardColumn{Key: "", Label: "PERF"},
	dashboardColumn{Key: "", Label: "SECURITY"},
)

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

func trendURLFor(componentID uuid.UUID) string {
	if !showTrendColumn {
		return ""
	}
	return "/components/" + componentID.String() + "/sparkline"
}

// firstMetricCol mirrors gridTemplateFor's leading-column count: #, name,
// score, and optionally trend.
func firstMetricCol(trend bool) int {
	if trend {
		return 4
	}
	return 3
}
