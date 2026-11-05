package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// sonarDetailView is one binding's SonarQube snapshot, flattened for the
// template — field list ported from the legacy Lotus's SonarView
// (handlers/sonar.go).
type sonarDetailView struct {
	HasData bool

	QualityGate     string
	QualityGatePass bool
	// Gate is the quality gate as a pill ("passed"/"failed"), worded as
	// the project's SonarQube page words it.
	Gate badgeCell
	// Ratings are reliability, security and maintainability as pills.
	Ratings               []sonarRating
	Coverage              string
	CoverageFailed        bool
	Bugs                  string
	BugsFailed            bool
	Vulnerabilities       string
	VulnerabilitiesFailed bool
	CodeSmells            string
	CodeSmellsFailed      bool
	SecurityHotspots      string
	Duplications          string
	DuplicationsFailed    bool
	NCLOC                 string
	Complexity            string
	Reliability           string
	Security              string
	Maintainability       string
	TestDuration          string
	Languages             string
	LastScanAt            string
	CapturedAt            string
	// Breakdown is how the headline score was computed.
	Breakdown *scoreBreakdown
	// Conditions are the quality gate's conditions, the failing ones
	// first; ConditionsMissing marks a failed gate whose snapshot was
	// taken before conditions were recorded.
	Conditions        []sonarCondition
	ConditionsMissing bool
}

// sonarCondition is one quality-gate condition as the page words it:
// "New coverage · 82.5% · needs at least 85%".
type sonarCondition struct {
	Metric, Actual, Threshold string
	Failed                    bool
}

// sonarConditions reads the gate's conditions from a snapshot's raw_json
// (the collector's Measures), failing ones first.
func sonarConditions(raw []byte) []sonarCondition {
	if len(raw) == 0 {
		return nil
	}
	var m sonar.Measures
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]sonarCondition, 0, len(m.GateConditions))
	for _, g := range m.GateConditions {
		c := sonarCondition{Metric: sonarMetricLabel(g.MetricKey), Actual: sonarMetricValue(g.MetricKey, g.ActualValue), Failed: g.Failed()}
		limit := sonarMetricValue(g.MetricKey, g.ErrorThreshold)
		switch {
		case strings.HasSuffix(g.MetricKey, "_rating"):
			// SonarQube fails a rating worse than the threshold (GT 1 is
			// "worse than A").
			c.Threshold = "needs " + limit + " or better"
		case g.Comparator == "LT":
			c.Threshold = "needs at least " + limit
		case g.Comparator == "GT":
			c.Threshold = "needs at most " + limit
		default:
			c.Threshold = "threshold " + limit
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Failed && !out[j].Failed })
	return out
}

// sonarMetricLabel names a SonarQube metric key: "new_coverage" is "New
// coverage", "new_reliability_rating" is "New reliability rating".
func sonarMetricLabel(key string) string {
	if key == "" {
		return "–"
	}
	return capitalize(strings.ReplaceAll(key, "_", " "))
}

// sonarMetricValue writes a condition value as the page writes the
// metric: a rating as its letter (SonarQube sends 1–5), a percentage with
// its sign, anything else as sent.
func sonarMetricValue(key, v string) string {
	if v == "" {
		return "–"
	}
	switch {
	case strings.HasSuffix(key, "_rating"):
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 1 && f <= 5 {
			return string(rune('A' + int(f) - 1))
		}
	case strings.Contains(key, "coverage") || strings.Contains(key, "density") || strings.Contains(key, "percent") || strings.HasSuffix(key, "_reviewed"):
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64) + "%"
		}
	}
	return v
}

// sonarRating is one SonarQube rating, labelled, as its letter's pill.
type sonarRating struct {
	Label string
	Badge badgeCell
}

// sonarPct/sonarInt/sonarFloat render a nullable Sonar metric the same
// "–" when absent, otherwise formatted, pattern the legacy pct()/intStr()
// helpers used — kept here rather than shared with GitLab's helpers
// since the two kinds' nullable-field shapes don't overlap.
func sonarPct(v *float64) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

// sonarInt writes a count with comma separators ("18,250"), as the
// project's SonarQube page does.
func sonarInt(v *int) string {
	if v == nil {
		return "–"
	}
	return thousands(int64(*v))
}

func sonarStr(v *string) string {
	if v == nil || *v == "" {
		return "–"
	}
	return *v
}

// humanDuration formats a millisecond unit-test execution time — ported
// verbatim from the legacy handlers/sonar.go.
func humanDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000
	if sec < 60 {
		return strconv.FormatFloat(sec, 'f', 1, 64) + "s"
	}
	m := int64(sec) / 60
	s := int64(sec) % 60
	return fmt.Sprintf("%dm %ds", m, s)
}

// Grade-band thresholds ported verbatim from the legacy Lotus's
// buildSonarViews (handlers/sonar.go) — coverage < 90%, > 100 code
// smells, > 30% duplication, or any bug/vulnerability at all reads as
// failing on the detail page the same way it did on the old fleet table.
func buildSonarDetailView(snap *repo.SonarSnapshot) sonarDetailView {
	v := sonarDetailView{
		QualityGate: "–", Coverage: "–", Bugs: "–", Vulnerabilities: "–", CodeSmells: "–",
		SecurityHotspots: "–", Duplications: "–", NCLOC: "–", Complexity: "–",
		Reliability: "–", Security: "–", Maintainability: "–", TestDuration: "–",
		Languages: "–", LastScanAt: "–",
	}
	if snap == nil {
		return v
	}
	v.HasData = true
	v.CapturedAt = relativeTimeStr(snap.CapturedAt)

	v.QualityGate = sonarStr(snap.QualityGate)
	v.QualityGatePass = snap.QualityGate != nil && *snap.QualityGate == "OK"
	switch v.QualityGate {
	case "OK":
		v.Gate = badgeCell{Text: "Quality gate passed", Tone: "pass"}
	case "ERROR", "WARN":
		v.Gate = badgeCell{Text: "Quality gate failed", Tone: "fail"}
	default:
		v.Gate = badgeCell{Text: "No quality gate", Tone: "none"}
	}

	v.Coverage = sonarPct(snap.Coverage)
	v.CoverageFailed = snap.Coverage != nil && *snap.Coverage < 90

	v.Bugs = sonarInt(snap.Bugs)
	v.BugsFailed = snap.Bugs != nil && *snap.Bugs > 0

	v.Vulnerabilities = sonarInt(snap.Vulnerabilities)
	v.VulnerabilitiesFailed = snap.Vulnerabilities != nil && *snap.Vulnerabilities > 0

	v.CodeSmells = sonarInt(snap.CodeSmells)
	v.CodeSmellsFailed = snap.CodeSmells != nil && *snap.CodeSmells > 100

	v.SecurityHotspots = sonarInt(snap.SecurityHotspots)

	v.Duplications = sonarPct(snap.DuplicatedLinesDensity)
	v.DuplicationsFailed = snap.DuplicatedLinesDensity != nil && *snap.DuplicatedLinesDensity > 30

	v.NCLOC = sonarInt(snap.NCLOC)
	v.Complexity = sonarInt(snap.Complexity)
	v.Reliability = sonarStr(snap.ReliabilityRating)
	v.Security = sonarStr(snap.SecurityRating)
	v.Maintainability = sonarStr(snap.SqaleRating)
	for _, r := range []struct{ label, letter string }{
		{"Reliability", v.Reliability}, {"Security", v.Security}, {"Maintainability", v.Maintainability},
	} {
		v.Ratings = append(v.Ratings, sonarRating{Label: r.label, Badge: sonarGrade(r.letter, r.label)})
	}
	v.Languages = sonarStr(snap.Languages)

	if snap.TestExecutionTimeMs != nil {
		v.TestDuration = humanDuration(int64(*snap.TestExecutionTimeMs))
	}
	if snap.LastScanAt != nil {
		v.LastScanAt = relativeTimeStr(*snap.LastScanAt)
	}
	v.Conditions = sonarConditions(snap.RawJSON)
	v.ConditionsMissing = len(v.Conditions) == 0 && (v.QualityGate == "ERROR" || v.QualityGate == "WARN")
	return v
}

func renderSonarDetail(c *fiber.Ctx, d Deps, s tenant.Scope, chrome metricChrome, org *httpx.Org, user *httpx.User, nav *httpx.Nav) error {
	selectedDate := strings.TrimSpace(c.Query("date"))
	var asOf *time.Time
	if selectedDate != "" {
		if _, err := time.Parse("2006-01-02", selectedDate); err != nil {
			selectedDate = "" // unparseable — fall back to the latest
		} else if cutoff, err := repo.EndOfSnapshotDay(c.Context(), s, selectedDate); err != nil {
			return writeRepoErr(c, err)
		} else {
			asOf = &cutoff
		}
	}

	snap, err := repo.SonarSnapshotAt(c.Context(), s, chrome.Binding.ID, asOf)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return writeRepoErr(c, err)
	}
	if errors.Is(err, repo.ErrNotFound) {
		snap = nil
	}
	dates, err := repo.SonarSnapshotDatesForBinding(c.Context(), s, chrome.Binding.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	view := buildSonarDetailView(snap)
	view.Breakdown = sonarBreakdown(snap)
	cards, err := buildMetricChartCards(c.Context(), s, chrome.Binding.ID, chrome.Binding.Kind, "7d")
	if err != nil {
		return writeRepoErr(c, err)
	}

	c.Type("html")
	return renderPage(c, d, "pages/metric_sonar.html", httpx.ViewData{
		Title: projectTitle(&chrome.Project, chrome.Component.Name, kindLabels["sonarqube"]), Org: org, User: user, Nav: nav,
		Data: struct {
			Chrome       metricChrome
			View         sonarDetailView
			Dates        []string
			SelectedDate string
			Charts       chartGridData
		}{Chrome: chrome, View: view, Dates: dates, SelectedDate: selectedDate, Charts: newChartGridData(cards, "7d", chrome.BasePath())},
	})
}
