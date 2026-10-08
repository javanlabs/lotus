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
	// Bar is the MeterBar under the number, nil for none.
	Bar []barSegment
	// Info, when set, is the tab's "how is this calculated" popover.
	Info string
	// Title is the link's hover text (a stale integration says why here).
	Title string
	// Muted draws a tab that has nothing to show yet: an unbound kind.
	Muted bool
	// Lead widens the tab, as the KPI row has always drawn the score.
	Lead bool
}

// barSegment is one coloured run of a MeterBar; Tone "rest" is the
// unfilled remainder.
type barSegment struct {
	Tone string
	Flex int
}

// meter is a MeterBar filled to pct in tone.
func meter(tone string, pct int) []barSegment {
	pct = clamp(pct, 0, 100)
	var out []barSegment
	if pct > 0 {
		out = append(out, barSegment{Tone: tone, Flex: pct})
	}
	if pct < 100 {
		out = append(out, barSegment{Tone: "rest", Flex: 100 - pct})
	}
	return out
}

// projectTabs is a project's metric strip: its Lotus score, then one tab
// per kind's pass rate, in bindableKinds order (kpiDefs is asserted to
// follow it). Both come from projectSummary.
func projectTabs(score scoreCard, kpis []kpiCard) []metricTab {
	tabs := make([]metricTab, 0, len(kpis)+1)
	st := metricTab{
		Key: "score", Label: "LOTUS SCORE", Href: score.Href, Value: "–", Unit: "/100", ValueTone: "brand", Lead: true,
		Sub:  fmt.Sprintf("%d healthy · %d failing", score.HealthyN, score.FailingN),
		Info: score.Info,
	}
	if score.HasScore {
		st.Value = strconv.Itoa(score.Score)
	}
	for _, seg := range []barSegment{{"pass", score.HealthyPct}, {"fail", score.FailingPct}} {
		if seg.Flex > 0 {
			st.Bar = append(st.Bar, seg)
		}
	}
	tabs = append(tabs, st)
	for _, k := range kpis {
		pct, _ := strconv.Atoi(strings.TrimSuffix(k.Pct, "%"))
		tabs = append(tabs, metricTab{
			Key: k.Key, Label: k.Label, Href: k.Href, Value: k.Value, Unit: k.Unit, ValueTone: k.Tone,
			Sub: k.Sub, Bar: meter(k.Tone, pct), Info: k.Info,
		})
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
				tab.Bar = meter(cell.Tone, *kh.Score)
				tab.Sub = cell.Title
			} else {
				// No score: the status word is the number, and the
				// collector's own phrasing the note under it.
				tab.Value = statusWord(collector.Status(kh.Status))
				tab.Sub = headlineText(kh)
			}
			at := kindSyncedAt(*metric, kh)
			if note := staleNote(k, at, now); note != "" {
				tab.Title = note
				tab.Sub = "Stale · synced " + relativeTimeStrAt(at, now)
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
