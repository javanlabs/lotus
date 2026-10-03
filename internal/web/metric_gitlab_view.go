package web

import (
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// gitlabDetailView is the legacy gitlab.html list row (see that file's
// GitlabView), reshaped as one binding's detail — task 4.4's own note
// that GitLab "never had a detail page before" is why this has no old
// per-repo template to port markup from, only the field list a repo's
// row already showed on the fleet table.
type gitlabDetailView struct {
	HasData bool

	Language      string
	DefaultBranch string
	WebURL        string

	BranchesTotal, BranchesActive, BranchesStale int

	PipelineStatus   string
	PipelineTone     string
	PipelineWebURL   string
	LastDuration     string
	PipelineFinished string
	SuccessRate      string

	LastActivity  string
	LastCommitter string
	Commits30d    string

	Protected  bool
	Visibility string
	Archived   bool
}

func buildGitlabDetailView(snap *repo.GitlabSnapshot) gitlabDetailView {
	v := gitlabDetailView{
		DefaultBranch: "–", LastDuration: "–", PipelineFinished: "–", SuccessRate: "–", LastActivity: "–", Commits30d: "–",
		PipelineTone: "none",
	}
	if snap == nil {
		return v
	}
	v.HasData = true

	if snap.PrimaryLanguage != nil {
		v.Language = *snap.PrimaryLanguage
	}
	if snap.DefaultBranch != nil {
		v.DefaultBranch = *snap.DefaultBranch
	}
	if snap.WebURL != nil {
		v.WebURL = *snap.WebURL
	}
	if snap.BranchesTotal != nil {
		v.BranchesTotal = *snap.BranchesTotal
	}
	if snap.BranchesActive != nil {
		v.BranchesActive = *snap.BranchesActive
	}
	if snap.BranchesStale != nil {
		v.BranchesStale = *snap.BranchesStale
	}

	if snap.LastPipelineStatus != nil {
		v.PipelineStatus = *snap.LastPipelineStatus
		switch v.PipelineStatus {
		case "success":
			v.PipelineTone = "pass"
		case "failed":
			v.PipelineTone = "fail"
		case "running", "pending":
			// In flight, so no verdict yet — not a failure.
			v.PipelineTone = "none"
		}
	}
	if snap.LastPipelineWebURL != nil {
		v.PipelineWebURL = *snap.LastPipelineWebURL
	}
	if snap.LastPipelineDurationSec != nil {
		v.LastDuration = fmtDuration(*snap.LastPipelineDurationSec)
	}
	if snap.LastPipelineFinishedAt != nil {
		v.PipelineFinished = relativeTimeStr(*snap.LastPipelineFinishedAt)
	}
	if snap.PipelineSuccessRate != nil && snap.PipelineSampleSize != nil && *snap.PipelineSampleSize > 0 {
		rate := *snap.PipelineSuccessRate
		n := *snap.PipelineSampleSize
		pass := int(rate*float64(n) + 0.5)
		v.SuccessRate = fmt.Sprintf("%d%% (%d/%d)", int(rate*100+0.5), pass, n)
	}

	if snap.LastActivityAt != nil {
		v.LastActivity = relativeTimeStr(*snap.LastActivityAt)
	}
	if snap.LastCommitterName != nil {
		v.LastCommitter = *snap.LastCommitterName
	}
	if snap.Commits30d != nil {
		if snap.Commits30dCapped != nil && *snap.Commits30dCapped {
			v.Commits30d = "100+"
		} else {
			v.Commits30d = fmt.Sprintf("%d", *snap.Commits30d)
		}
	}

	v.Protected = snap.DefaultBranchProtected != nil && *snap.DefaultBranchProtected
	if snap.Visibility != nil {
		v.Visibility = *snap.Visibility
	}
	v.Archived = snap.Archived != nil && *snap.Archived
	return v
}

// fmtDuration formats a pipeline duration (seconds) as "2m 13s" / "47s" —
// ported verbatim from the legacy handlers/gitlab.go.
func fmtDuration(sec int) string {
	if sec <= 0 {
		return "–"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	m := sec / 60
	sRem := sec % 60
	if m >= 60 {
		h := m / 60
		m = m % 60
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm %ds", m, sRem)
}

func renderGitlabDetail(c *fiber.Ctx, d Deps, s tenant.Scope, chrome metricChrome, org *httpx.Org, user *httpx.User, nav *httpx.Nav) error {
	snap, err := repo.LatestGitlabSnapshot(c.Context(), s, chrome.Binding.ID)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return writeRepoErr(c, err)
	}
	if errors.Is(err, repo.ErrNotFound) {
		snap = nil
	}
	view := buildGitlabDetailView(snap)
	cards, err := buildMetricChartCards(c.Context(), s, chrome.Binding.ID, chrome.Binding.Kind, "7d")
	if err != nil {
		return writeRepoErr(c, err)
	}

	c.Type("html")
	return renderPage(c, d, "pages/metric_gitlab.html", httpx.ViewData{
		Title: chrome.Component.Name + " · GitLab", Org: org, User: user, Nav: nav,
		Data: struct {
			Chrome metricChrome
			View   gitlabDetailView
			Charts chartGridData
		}{Chrome: chrome, View: view, Charts: newChartGridData(cards, "7d", chrome.BasePath())},
	})
}
