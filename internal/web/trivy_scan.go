package web

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/scheduler"
	"github.com/uyab/lotus/internal/tenant"
)

// scanStatus is a repository scan's bar on the Trivy page
// (components/trivy_scan_status.html): what the scan is doing now, how
// the last one went, and the button that starts one.
type scanStatus struct {
	BasePath string
	// State is "waiting" (queued for a worker), "scanning" (a worker has
	// it) or "idle". Active is either of the first two: the bar polls
	// while it is set and the button is off.
	State  string
	Active bool
	// Since is how long the run has been in State, for an active one.
	Since string
	// Last describes the last finished scan; LastFailed marks it red and
	// Error is why.
	Last       string
	LastFailed bool
	Error      string
	// Next is when the daily scan runs again, for an idle one.
	Next string
	// CanScan is whether the viewer may start one (sync.trigger).
	CanScan bool
}

// loadScanStatus reads chrome's binding's runs into a scanStatus.
func loadScanStatus(ctx context.Context, d Deps, chrome *metricChrome, canScan bool) (scanStatus, error) {
	st := scanStatus{BasePath: chrome.BasePath(), State: "idle", CanScan: canScan}
	runs, err := global.ListRunsForBinding(ctx, d.DB, chrome.Binding.OrgID, chrome.Binding.ID, 10)
	if err != nil {
		return st, err
	}
	now := time.Now()
	for i, r := range runs {
		if i == 0 {
			switch {
			case r.Status == "running":
				st.State, st.Active = "scanning", true
				st.Since = relativeTimeStrAt(derefTime(r.StartedAt, r.CreatedAt), now)
			case r.Status == "leased" || (r.Status == "queued" && !r.ScheduledFor.After(now.Add(5*time.Second))):
				st.State, st.Active = "waiting", true
				st.Since = relativeTimeStrAt(r.CreatedAt, now)
			}
		}
		if r.FinishedAt == nil {
			continue
		}
		st.LastFailed = r.Status == "failed"
		if st.LastFailed {
			st.Last = "Last scan failed " + relativeTimeStrAt(*r.FinishedAt, now)
			if r.Error != nil {
				st.Error = *r.Error
			}
		} else {
			st.Last = "Last scanned " + relativeTimeStrAt(*r.FinishedAt, now)
		}
		break
	}
	if !st.Active && chrome.Binding.NextSyncAt != nil && chrome.Binding.Enabled {
		st.Next = "next scan " + untilStr(*chrome.Binding.NextSyncAt, now)
	}
	return st, nil
}

func derefTime(t *time.Time, fallback time.Time) time.Time {
	if t == nil {
		return fallback
	}
	return *t
}

// untilStr is relativeTimeStrAt looking forward: "in 3 hr".
func untilStr(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "any moment"
	case d < time.Hour:
		return fmt.Sprintf("in %d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("in %d hr", int(d.Hours()))
	}
}

// startRepoScan queues a repository scan now and answers with the bar,
// which then polls until the scan is done. A scan already queued — the
// daily one, or one waiting out a retry — is moved up rather than
// duplicated.
func startRepoScan(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if !repo.IsRepoScan(&chrome.Binding) {
			return fiber.ErrNotFound
		}
		b := chrome.Binding
		if err := scheduler.EnqueueImmediate(c.Context(), d.DB, b.OrgID, b.ConnectionID, b.ID, b.Kind); err != nil {
			return writeRepoErr(c, err)
		}
		if err := global.RunQueuedNow(c.Context(), d.DB, b.OrgID, b.ID); err != nil {
			return writeRepoErr(c, err)
		}
		return renderScanStatus(c, d, chrome)
	}
}

// repoScanStatus is the bar on its own, for its polling. When a scan the
// page was waiting on has finished (was=active, and nothing is active
// now), it asks htmx to reload the page, so the new findings show.
func repoScanStatus(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		chrome, err := resolveMetricChrome(c, s)
		if err != nil {
			return writeRepoErr(c, err)
		}
		if !repo.IsRepoScan(&chrome.Binding) {
			return fiber.ErrNotFound
		}
		return renderScanStatus(c, d, chrome)
	}
}

func renderScanStatus(c *fiber.Ctx, d Deps, chrome *metricChrome) error {
	st, err := loadScanStatus(c.Context(), d, chrome, canTriggerSync(c))
	if err != nil {
		return writeRepoErr(c, err)
	}
	if c.Query("was") == "active" && !st.Active {
		c.Set("HX-Refresh", "true")
	}
	c.Type("html")
	return d.Renderer.RenderNamed(c, "trivy_scan_status", st)
}

func canTriggerSync(c *fiber.Ctx) bool {
	p, ok := authz.CurrentPrincipal(c)
	return ok && p.Can("sync.trigger")
}

func registerRepoScanRoutes(app *fiber.App, base string, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Post(base+"/scan", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("sync.trigger"), startRepoScan(d))
	app.Get(base+"/scan-status", tenant.RequireTenant(), requireSession, loadPrincipal,
		authz.Require("dashboard.read"), repoScanStatus(d))
}
