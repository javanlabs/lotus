package web

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// sonarDetailView is one binding's SonarQube snapshot, flattened for the
// template — field list ported from the legacy Lotus's SonarView
// (handlers/sonar.go).
type sonarDetailView struct {
	HasData bool

	QualityGate           string
	QualityGatePass       bool
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

func sonarInt(v *int) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%d", *v)
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
	v.Languages = sonarStr(snap.Languages)

	if snap.TestExecutionTimeMs != nil {
		v.TestDuration = humanDuration(int64(*snap.TestExecutionTimeMs))
	}
	if snap.LastScanAt != nil {
		v.LastScanAt = relativeTimeStr(*snap.LastScanAt)
	}
	return v
}

func renderSonarDetail(c *fiber.Ctx, d Deps, s tenant.Scope, chrome metricChrome, org *httpx.Org, user *httpx.User, nav *httpx.Nav) error {
	selectedDate := strings.TrimSpace(c.Query("date"))
	var asOf *time.Time
	if selectedDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", selectedDate, time.Local); err == nil {
			cutoff := t.Add(24*time.Hour - time.Nanosecond)
			asOf = &cutoff
		} else {
			selectedDate = "" // invalid date param — fall back to latest, same as the legacy parseAsOf
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

	c.Type("html")
	return d.Renderer.Render(c, "pages/metric_sonar.html", httpx.ViewData{
		Title: chrome.Component.Name + " · SonarQube", Org: org, User: user, Nav: nav,
		Data: struct {
			Chrome       metricChrome
			View         sonarDetailView
			Dates        []string
			SelectedDate string
		}{Chrome: chrome, View: view, Dates: dates, SelectedDate: selectedDate},
	})
}
