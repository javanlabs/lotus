package web

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// sentryIssueRow is one Sentry issue, flattened for the template — field
// list ported from the legacy Lotus's SentryIssueView (handlers/sentry.go).
type sentryIssueRow struct {
	ShortID    string
	Title      string
	Status     string
	Substatus  string
	EventCount int
	FirstSeen  string
	LastSeen   string
	Permalink  string
}

type sentryStatusOption struct {
	Value    string
	Count    int
	Selected bool
	// Label is Value as a figure's heading: "ARCHIVED UNTIL ESCALATING".
	Label string
}

// sentryStatusOptions turns a substatus→count map into a stable, sorted
// list for the filter dropdown — ported verbatim from the legacy Lotus's
// own function of the same name (handlers/sentry.go): common substatuses
// first in a fixed order, then whatever else appears.
func sentryStatusOptions(counts map[string]int, selected string) []sentryStatusOption {
	order := []string{"new", "ongoing", "escalating", "regressed", "archived_until_escalating"}
	seen := map[string]bool{}
	out := make([]sentryStatusOption, 0, len(counts))
	add := func(k string) {
		if seen[k] {
			return
		}
		if n, ok := counts[k]; ok {
			out = append(out, sentryStatusOption{Value: k, Count: n, Selected: k == selected,
				Label: strings.ToUpper(strings.ReplaceAll(k, "_", " "))})
			seen[k] = true
		}
	}
	for _, k := range order {
		add(k)
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k)
	}
	return out
}

func renderSentryDetail(c *fiber.Ctx, d Deps, s tenant.Scope, chrome metricChrome, org *httpx.Org, user *httpx.User, nav *httpx.Nav) error {
	// ?date= reads the history as SonarQube's page does: the state at the
	// end of that day. Anything unparseable falls back to the latest.
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
	snaps, err := repo.SentrySnapshotsAt(c.Context(), s, chrome.Binding.ID, asOf)
	if err != nil {
		return writeRepoErr(c, err)
	}
	dates, err := repo.SentrySnapshotDatesForBinding(c.Context(), s, chrome.Binding.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}

	byEnv := make(map[string]repo.SentrySnapshot, len(snaps))
	for _, snap := range snaps {
		byEnv[snap.Environment] = snap
	}
	envs := sentryEnvOrder(snaps)

	selectedEnv := strings.TrimSpace(c.Query("env"))
	if _, ok := byEnv[selectedEnv]; !ok {
		selectedEnv = ""
		if len(envs) > 0 {
			selectedEnv = envs[0]
		}
	}

	cards, err := buildMetricChartCards(c.Context(), s, chrome.Binding.ID, chrome.Binding.Kind, "7d")
	if err != nil {
		return writeRepoErr(c, err)
	}

	data := struct {
		Chrome       metricChrome
		HasData      bool
		Environments []string
		SelectedEnv  string
		ProjectSlug  string
		ProjectName  string
		Platform     string
		TeamSlug     string
		TotalIssues  int
		NoSignal     bool
		CapturedAt   string
		ScoreText    string
		Tone         string
		// Breakdown is how the environment's score was computed; the
		// first environment is the one the headline came from.
		Breakdown      *scoreBreakdown
		Statuses       []sentryStatusOption
		SelectedStatus string
		Rows           []sentryIssueRow
		Charts         chartGridData
		Dates          []string
		SelectedDate   string
	}{Chrome: chrome, Charts: newChartGridData(cards, "7d", chrome.BasePath()),
		Dates: dates, SelectedDate: selectedDate}

	if selectedEnv == "" {
		c.Type("html")
		return renderPage(c, d, "pages/metric_sentry.html", httpx.ViewData{
			Title: projectTitle(&chrome.Project, chrome.Component.Name, kindLabels["sentry"]), Org: org, User: user, Nav: nav, Data: data,
		})
	}

	snap := byEnv[selectedEnv]
	data.Chrome.SentryEnvs = []repo.SentryEnvironmentSummary{{SentrySnapshot: snap}}
	data.HasData = true
	data.Environments = envs
	data.SelectedEnv = selectedEnv
	data.ProjectSlug = snap.ProjectSlug
	data.ProjectName = snap.ProjectName
	data.Platform = snap.Platform
	data.TeamSlug = snap.TeamSlug
	data.TotalIssues = snap.TotalIssues
	// No score: nothing was measured, as opposed to a clean 100.
	data.NoSignal = snap.Score == nil
	data.CapturedAt = relativeTimeStr(snap.CapturedAt)
	data.ScoreText, data.Tone = "–", bandTone(snap.Score)
	if snap.Score != nil {
		data.ScoreText = strconv.Itoa(*snap.Score)
	}
	data.Breakdown = sentryBreakdown(snap)

	issues, err := repo.ListSentryIssuesForSnapshot(c.Context(), s, snap.ID)
	if err != nil {
		return writeRepoErr(c, err)
	}
	counts := map[string]int{}
	for _, is := range issues {
		counts[is.Substatus]++
	}
	selectedStatus := strings.TrimSpace(c.Query("status"))
	if _, ok := counts[selectedStatus]; !ok {
		selectedStatus = ""
	}
	data.Statuses = sentryStatusOptions(counts, selectedStatus)
	data.SelectedStatus = selectedStatus

	rows := make([]sentryIssueRow, 0, len(issues))
	for _, is := range issues {
		if selectedStatus != "" && is.Substatus != selectedStatus {
			continue
		}
		row := sentryIssueRow{
			ShortID: is.ShortID, Title: is.Title, Status: is.Status, Substatus: is.Substatus,
			EventCount: is.EventCount, Permalink: is.Permalink, FirstSeen: "–", LastSeen: "–",
		}
		if is.FirstSeen != nil {
			row.FirstSeen = relativeTimeStr(*is.FirstSeen)
		}
		if is.LastSeen != nil {
			row.LastSeen = relativeTimeStr(*is.LastSeen)
		}
		rows = append(rows, row)
	}
	data.Rows = rows

	c.Type("html")
	return renderPage(c, d, "pages/metric_sentry.html", httpx.ViewData{
		Title: projectTitle(&chrome.Project, chrome.Component.Name, kindLabels["sentry"]), Org: org, User: user, Nav: nav, Data: data,
	})
}

// sentryEnvOrder lists the environments with the one the headline came
// from first — the worst, as the collector picks it (lowest score; one
// with no signal only when none has a score) — then the rest by name. The
// page opens on it, so the score it leads with is the tab's number.
func sentryEnvOrder(snaps []repo.SentrySnapshot) []string {
	worst := -1
	for i, s := range snaps {
		switch {
		case worst < 0:
			worst = i
		case s.Score != nil && (snaps[worst].Score == nil || *s.Score < *snaps[worst].Score):
			worst = i
		case s.Score != nil && snaps[worst].Score != nil && *s.Score == *snaps[worst].Score && s.Environment < snaps[worst].Environment:
			worst = i
		case s.Score == nil && snaps[worst].Score == nil && s.Environment < snaps[worst].Environment:
			worst = i
		}
	}
	envs := make([]string, 0, len(snaps))
	for i, s := range snaps {
		if i != worst {
			envs = append(envs, s.Environment)
		}
	}
	sort.Strings(envs)
	if worst >= 0 {
		envs = append([]string{snaps[worst].Environment}, envs...)
	}
	return envs
}
