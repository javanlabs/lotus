package web

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// metricTab is one tab of a metric strip (components/metric_tabs.html):
// a link carrying the number it opens. The project's strip (its score and
// one tab per kind's pass rate) and a component's (its score and one tab
// per kind's own headline) are both lists of these, so the two draw the
// same way and cannot drift apart.
type metricTab struct {
	// Key is what the page's Active is compared with: "score" or a kind.
	// Href is "" for a tab that opens nothing (an unbound kind); it is
	// drawn, but not as a link.
	Key, Label, Href string
	Value, Unit      string
	// ValueTone colours the number: pass|fail|none, or brand for a score.
	ValueTone string
	Sub       string
	// Verdict is the chip under the number: the tab's verdict as a glyph
	// and a word, so pass and fail never rest on colour alone. Nil draws
	// an empty slot of the same height (nothing to judge yet), so the
	// strip keeps one height whichever tabs have a verdict.
	Verdict *tabVerdict
	// Info, when set, is the tab's "how is this calculated" popover.
	Info string
	// Title is the link's hover text (a stale integration says why here).
	Title string
	// Muted draws a tab that has nothing to show yet: an unbound kind.
	Muted bool
	// Lead widens the tab, as the KPI row has always drawn the score.
	Lead bool
}

// tabVerdict is a tab's verdict chip. It replaced a MeterBar filled to the
// number above it, which only repeated that number: a component's pass or
// fail is not a score threshold (SonarQube's is its quality gate, Trivy's
// its fail_on severity), so the fill could not say whether a tab passed —
// only its colour did. The chip says it in a word and a glyph, and where
// there is a target (a pass rate's passRateTarget) how far off it is.
type tabVerdict struct {
	// Tone is pass|fail; it picks the glyph (a check or a cross) as well
	// as the colour.
	Tone string
	Text string
	// Stale dashes the chip, as a stale cell is dashed in the component
	// table: the verdict is the last one synced, not a current one.
	Stale bool
}

// rateVerdict is a pass rate's chip: whether it meets passRateTarget, or
// by how many points it misses it.
func rateVerdict(pct int) *tabVerdict {
	if pct >= passRateTarget {
		return &tabVerdict{Tone: "pass", Text: fmt.Sprintf("Meets %d%% target", passRateTarget)}
	}
	gap, unit := passRateTarget-pct, "pts"
	if gap == 1 {
		unit = "pt"
	}
	return &tabVerdict{Tone: "fail", Text: fmt.Sprintf("%d %s below %d%%", gap, unit, passRateTarget)}
}

// scoreVerdict is the Lotus score's chip: how many components fail — what
// an averaged score can hide.
func scoreVerdict(score scoreCard) *tabVerdict {
	switch {
	case score.FailingN > 0:
		return &tabVerdict{Tone: "fail", Text: fmt.Sprintf("%d failing", score.FailingN)}
	case score.HealthyN > 0:
		return &tabVerdict{Tone: "pass", Text: "All healthy"}
	}
	return nil
}

// statusVerdict is a component tab's chip for a kind that has a score:
// the number is the score, so the chip carries the verdict. A status that
// is neither pass nor fail draws none.
func statusVerdict(tone string) *tabVerdict {
	switch tone {
	case "pass":
		return &tabVerdict{Tone: "pass", Text: "Passing"}
	case "fail":
		return &tabVerdict{Tone: "fail", Text: "Failing"}
	}
	return nil
}

// projectTabs is a project's metric strip: its Lotus score, then one tab
// per kind's pass rate, in bindableKinds order (kpiDefs is asserted to
// follow it). Both come from projectSummary.
func projectTabs(score scoreCard, kpis []kpiCard) []metricTab {
	tabs := make([]metricTab, 0, len(kpis)+1)
	st := metricTab{
		Key: "score", Label: "LOTUS SCORE", Href: score.Href, Value: "–", Unit: "/100", ValueTone: "brand", Lead: true,
		Sub:     "no data yet",
		Info:    score.Info,
		Verdict: scoreVerdict(score),
	}
	if total := score.HealthyN + score.FailingN; total > 0 {
		st.Sub = fmt.Sprintf("%d of %d components healthy", score.HealthyN, total)
	}
	if score.HasScore {
		st.Value = strconv.Itoa(score.Score)
	}
	tabs = append(tabs, st)
	for _, k := range kpis {
		tab := metricTab{
			Key: k.Key, Label: k.Label, Href: k.Href, Value: k.Value, Unit: k.Unit, ValueTone: k.Tone,
			Sub: k.Sub, Info: k.Info,
		}
		// "–" (no data yet) parses as nothing and draws no chip.
		if pct, err := strconv.Atoi(k.Value); err == nil {
			tab.Verdict = rateVerdict(pct)
		}
		tabs = append(tabs, tab)
	}
	return tabs
}

// MetricTabs is the project page's strip, for dashboard_block.
func (vm dashboardViewModel) MetricTabs() []metricTab { return projectTabs(vm.Score, vm.KPIs) }

// MetricTabs is an integration page's strip: the project page's, this
// kind's tab open.
func (p kindPage) MetricTabs() []metricTab { return projectTabs(p.Score, p.KPIs) }

// componentTabs is a component's metric strip: one tab per kind in
// bindableKinds order (AGENTS.md). A bound kind opens its metric page and
// shows the component's headline for it from the projection row — the
// same badgeFor the project's component table draws, so the tab and the
// table cell agree. An unbound kind keeps its place, muted and not a
// link, so the strip has one shape on every component; binding it is the
// header's Bind metric.
//
// bindings are the component's live bindings (one per kind at most,
// migration 00035); metric is its projection row, nil before any sync.
func componentTabs(project *repo.Project, comp *repo.Component, bindings []repo.Binding, metric *repo.ComponentMetric) []metricTab {
	byKind := make(map[string]repo.Binding, len(bindings))
	for _, b := range bindings {
		byKind[b.Kind] = b
	}
	var kinds map[string]repo.KindHeadline
	if metric != nil {
		kinds = metric.Metrics()
	}
	now := dashboardNow()

	tabs := make([]metricTab, 0, len(bindableKinds))
	for _, k := range bindableKinds {
		tab := metricTab{Key: k, Label: strings.ToUpper(kindLabels[k]), Value: "–", ValueTone: "none"}
		b, ok := byKind[k]
		if !ok {
			tab.Sub = "Not bound"
			tab.Muted = true
			tabs = append(tabs, tab)
			continue
		}
		tab.Href = metricURL(project, comp, k)
		kh, has := kinds[k]
		switch {
		case !has:
			tab.Sub = "Not synced yet"
		default:
			cell := badgeFor(kinds, k)
			tab.ValueTone = cell.Tone
			if kh.Score != nil {
				tab.Value, tab.Unit = strconv.Itoa(*kh.Score), "/100"
				tab.Sub = cell.Title
				tab.Verdict = statusVerdict(cell.Tone)
			} else {
				// No score: the status word is the number, and the
				// collector's own phrasing the note under it.
				tab.Value = statusWord(collector.Status(kh.Status))
				tab.Sub = headlineText(kh)
			}
			at := kindSyncedAt(*metric, kh)
			// A collector with nothing more to say than its status would
			// only repeat the chip (or the number); when it synced is
			// worth the line instead.
			if tab.Sub == statusWord(collector.Status(kh.Status)) {
				tab.Sub = "Synced " + relativeTimeStrAt(at, now)
			}
			if note := staleNote(k, at, now); note != "" {
				tab.Title = note
				tab.Sub = "Stale · synced " + relativeTimeStrAt(at, now)
				if tab.Verdict != nil {
					tab.Verdict.Stale = true
				}
			}
		}
		if !b.Enabled {
			tab.Sub = "Disabled · " + tab.Sub
		}
		tabs = append(tabs, tab)
	}
	return tabs
}

// loadComponentTabs reads what componentTabs draws from: the component's
// live bindings and its projection row.
func loadComponentTabs(c *fiber.Ctx, s tenant.Scope, project *repo.Project, comp *repo.Component) ([]metricTab, error) {
	bindings, err := repo.ListBindingsForComponent(c.Context(), s, comp.ID, false)
	if err != nil {
		return nil, err
	}
	metric, err := repo.GetComponentMetric(c.Context(), s, comp.ID)
	if errors.Is(err, repo.ErrNotFound) {
		metric, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	return componentTabs(project, comp, bindings, metric), nil
}

// firstBoundKind is the kind a component opens on: its first bound kind
// in bindableKinds order — SonarQube when it has one — or "" when it has
// bound nothing.
func firstBoundKind(bindings []repo.Binding) string {
	for _, k := range bindableKinds {
		for _, b := range bindings {
			if b.Kind == k {
				return k
			}
		}
	}
	return ""
}
