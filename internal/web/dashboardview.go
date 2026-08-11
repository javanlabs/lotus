package web

import (
	"fmt"
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
	N           int
	Name        string
	ProjectName string
	Score       string // "84" or "–"
	ScorePct    string // "84%" for the inline mini-bar width, "0%" if no score
	Tone        string // pass|warn|fail|none
	GitLab      badgeCell
	SonarQube   badgeCell
	Trivy       badgeCell
	Sentry      badgeCell
}

type badgeCell struct {
	Text string
	Tone string // pass|warn|fail|none
}

// kpiCard is one of the four fleet-wide rollup cards (Sonar gate, Trivy
// clean, Sentry health, Git hygiene in the mockup) — one per collector
// kind, showing the pass rate across every component that has that kind
// bound.
type kpiCard struct {
	Label string
	Value string // "89" or "–"
	Unit  string // "%"
	Pct   string // bar width, "89%"
	Tone  string
	Sub   string
}

// scoreCard is the large "Lotus Score" tile: the org-wide rollup score and
// status distribution across every component with a projection row.
type scoreCard struct {
	HasScore                          bool
	Score                             int
	HealthyPct, AtRiskPct, FailingPct int // segment widths, sum to 100 (0 each if NoData)
	HealthyN, AtRiskN, FailingN       int
}

type dashboardViewModel struct {
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
	case collector.StatusWarn:
		return "warn"
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
	return badgeCell{Text: headlineText(kh), Tone: toneOf(collector.Status(kh.Status))}
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
	var scores []*int
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
		scores = append(scores, score)

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
		rows = append(rows, dashboardRow{
			Name: comp.Name, ProjectName: comp.ProjectName,
			Score: scoreStr, ScorePct: scorePct, Tone: toneOf(status),
			GitLab: badgeFor(kindMap, "gitlab"), SonarQube: badgeFor(kindMap, "sonarqube"),
			Trivy: badgeFor(kindMap, "trivy"), Sentry: badgeFor(kindMap, "sentry"),
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		si, sj := scoreRank(rows[i].Score), scoreRank(rows[j].Score)
		if si != sj {
			return si < sj
		}
		return rows[i].Name < rows[j].Name
	})
	for i := range rows {
		rows[i].N = i + 1
	}

	vm := dashboardViewModel{
		ProjectCount: projectCount,
		Score:        buildScoreCard(statuses, scores),
		KPIs:         buildKPIs(kindStatuses, kindMeasured, len(components)),
		Rows:         rows,
		RowCount:     len(rows),
		Subtitle:     subtitle,
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

func buildScoreCard(statuses []collector.Status, scores []*int) scoreCard {
	score := repo.RollupScore(scores)
	healthy, atRisk, failing := 0, 0, 0
	for _, s := range statuses {
		switch s {
		case collector.StatusPass:
			healthy++
		case collector.StatusWarn:
			atRisk++
		case collector.StatusFail:
			failing++
		}
	}
	total := healthy + atRisk + failing
	sc := scoreCard{HealthyN: healthy, AtRiskN: atRisk, FailingN: failing}
	if score != nil {
		sc.HasScore = true
		sc.Score = *score
	}
	if total > 0 {
		sc.HealthyPct = healthy * 100 / total
		sc.AtRiskPct = atRisk * 100 / total
		sc.FailingPct = 100 - sc.HealthyPct - sc.AtRiskPct
	}
	return sc
}

var kpiDefs = []struct {
	Kind, Label string
}{
	{"sonarqube", "SONAR GATE"},
	{"trivy", "TRIVY CLEAN"},
	{"sentry", "SENTRY HEALTH"},
	{"gitlab", "GIT HYGIENE"},
}

func buildKPIs(kindStatuses map[string][]collector.Status, measured map[string]int, totalComponents int) []kpiCard {
	kpis := make([]kpiCard, 0, len(kpiDefs))
	for _, def := range kpiDefs {
		rate, ok := repo.PassRate(kindStatuses[def.Kind])
		card := kpiCard{Label: def.Label}
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
	case pct >= 50:
		return "warn"
	default:
		return "fail"
	}
}
