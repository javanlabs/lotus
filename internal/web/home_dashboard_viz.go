package web

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/scoring"
)

// The dashboard's visual half answers three questions, each with a form
// chosen for it: how healthy are we (a donut over component status), where
// do we start (ranked insights, a bar per integration and a component ×
// integration health map sorted worst first), and is it getting better
// (one 30-day trend per integration). All of it is derived here without
// I/O so it is tested without a database.

const (
	// homeTrendDays is how far back the trends look.
	homeTrendDays = 30
	// homeHealthMapCap is how many components the map lists at least; it is
	// sorted worst first, and a component with a failing check always stays,
	// so what falls off is only the ones with nothing failing.
	homeHealthMapCap = 25
	// homeNearWinNames is how many components a near-win insight names.
	homeNearWinNames = 3
)

// homeTrendDefs is one org-wide trend per integration, in bindableKinds'
// order. Findings add up across bindings; rates and scores average.
var homeTrendDefs = map[string]struct{ Label, Combine string }{
	"sonarqube": {"AVG COVERAGE", "avg"},
	"trivy":     {"TOTAL FINDINGS", "sum"},
	"sentry":    {"AVG HEALTH SCORE", "avg"},
	"gitlab":    {"AVG PIPELINE SUCCESS", "avg"},
}

// homeTrendCombine is what repo.OrgDailyTrends is asked for.
func homeTrendCombine() map[string]string {
	out := map[string]string{}
	for kind, def := range homeTrendDefs {
		out[primaryChartKey(kind)] = def.Combine
	}
	return out
}

// homeHealth is the donut: every component in exactly one state.
type homeHealth struct {
	Pass, Fail, None, Total                           int
	Pct                                               string // "41%", or "–" with nothing measured
	Tone                                              string
	PassLen, FailLen, NoneLen, FailOffset, NoneOffset float64 // of pathLength 100
}

// homeKindBar is one integration's average score.
type homeKindBar struct {
	Label, Info, Value, Width, Tone, Note string
}

// homeMapCell is one component's state for one integration.
type homeMapCell struct {
	Tone, Label, Title, Href string
	// Stale marks a value last synced too long ago to trust: it is still
	// shown, but must not read as the current state.
	Stale bool
}

type homeMapRow struct {
	Name, Project, Href, ProjectHref string
	Failing                          int
	Cells                            []homeMapCell
}

// homeInsightLink is a component or page an insight names.
type homeInsightLink struct{ Label, Href string }

// homeInsight is one reasoned suggestion: a figure, what it means, and
// where to act on it.
type homeInsight struct {
	Figure, Tone, Title, Detail string
	Links                       []homeInsightLink
}

// homeTrend is one small multiple.
type homeTrend struct {
	Label, Delta, DeltaTone string
	Chart                   chartSVG
	// change is the move relative to where it started, so trends in different
	// units can be compared: losing 37 of 82 points outranks losing 1% of 93%.
	change float64
}

type homeViz struct {
	HasComponents bool
	Health        homeHealth
	KindBars      []homeKindBar
	Insights      []homeInsight
	MapKinds      []string
	Map           []homeMapRow
	MoreMap       int
	Trends        []homeTrend
}

// homeMapCellLabel is the cell's text: the kind's score, 0–100, or "–"
// with no score. The colour is the score's band (bandTone), so the number
// and the colour always agree; the word for the state is in the title
// (homeMapCellTitle), and so are Trivy's counts and SonarQube's letter —
// a cell reads the same scale whichever kind it is. The status page's
// component cells draw the same.
func homeMapCellLabel(kh repo.KindHeadline) string {
	if kh.Score != nil {
		return strconv.Itoa(*kh.Score)
	}
	return "–"
}

// homeMapCellTitle is the cell's hover text: the kind, its score and band
// word (or why it has none), then what the score was made of — Trivy's
// finding counts ("1 critical · 5 high · 31 total"), SonarQube's own worst
// rating, the collector's count otherwise.
func homeMapCellTitle(kind string, kh repo.KindHeadline) string {
	parts := []string{}
	if kh.Score != nil {
		parts = append(parts, fmt.Sprintf("%d · %s", *kh.Score, scoring.Band(*kh.Score).Word))
	} else {
		parts = append(parts, reasonWord(kh))
	}
	switch {
	case kind == "trivy" && strings.Contains(kh.Summary, " critical, "):
		parts = append(parts, strings.ReplaceAll(kh.Summary, ", ", " · "))
	case kind == "sonarqube" && kh.Grade != "":
		parts = append(parts, "SonarQube worst rating "+kh.Grade)
	case kh.Score != nil && kh.CountValue != nil:
		parts = append(parts, headlineText(kh))
	}
	if kind == "sentry" && kh.Grade != "" {
		// A headline stored before letters were dropped still carries one.
		parts = append(parts, "grade "+kh.Grade)
	}
	return kindLabels[kind] + ": " + strings.Join(parts, " · ")
}

func buildHomeViz(in homeInputs, rates []kpiCard) homeViz {
	v := homeViz{HasComponents: len(in.Components) > 0, MapKinds: make([]string, 0, len(bindableKinds))}
	for _, k := range bindableKinds {
		v.MapKinds = append(v.MapKinds, kindLabels[k])
	}

	kindFails := map[string]int{}
	kindPass := map[string]int{}
	kindProjectFails := map[string]map[string]int{}
	projects := map[string]*repo.Project{}
	type projectTally struct {
		name       string
		pass, fail int
	}
	tally := map[string]*projectTally{}
	type nearWin struct {
		name, href, kind string
	}
	var nearWins []nearWin
	totalFails := 0
	rows := make([]homeMapRow, 0, len(in.Components))

	for _, c := range in.Components {
		project := &repo.Project{ID: c.ProjectID, Slug: c.ProjectSlug}
		comp := &repo.Component{ID: c.ID, Slug: c.Slug}
		projects[c.ProjectSlug] = project
		row := homeMapRow{Name: c.Name, Project: c.ProjectName, Href: componentURL(project, comp), ProjectHref: projectURL(project)}
		cm, has := in.Metrics[c.ID]
		var kinds map[string]repo.KindHeadline
		if has {
			kinds = cm.Metrics()
		}
		// A component is healthy, needs attention, or unscored by its own
		// score's band.
		var compScore *int
		if has {
			compScore = cm.Score
		}
		switch bandTone(compScore) {
		case "fail":
			v.Health.Fail++
		case "pass":
			v.Health.Pass++
		default:
			v.Health.None++
		}
		if tally[c.ProjectSlug] == nil {
			tally[c.ProjectSlug] = &projectTally{name: c.ProjectName}
		}
		failedKind := ""
		for _, k := range bindableKinds {
			kh, ok := kinds[k]
			if !ok {
				row.Cells = append(row.Cells, homeMapCell{Tone: "none", Label: "–", Title: kindLabels[k] + ": not connected"})
				continue
			}
			// Each kind's state is the band of its score: a red cell is a
			// score below 80, and that is what counts as failing here.
			tone := bandTone(kh.Score)
			switch tone {
			case "pass":
				kindPass[k]++
				tally[c.ProjectSlug].pass++
			case "fail":
				row.Failing++
				failedKind = k
				kindFails[k]++
				totalFails++
				tally[c.ProjectSlug].fail++
				if kindProjectFails[k] == nil {
					kindProjectFails[k] = map[string]int{}
				}
				kindProjectFails[k][c.ProjectSlug]++
			}
			title := homeMapCellTitle(k, kh)
			at := kindSyncedAt(cm, kh)
			stale := staleNote(k, at, in.Now) != ""
			if stale {
				title += " · last synced " + relativeTimeStrAt(at, in.Now)
			}
			row.Cells = append(row.Cells, homeMapCell{Tone: tone, Label: homeMapCellLabel(kh), Title: title, Href: metricURL(project, comp, k), Stale: stale})
		}
		if row.Failing == 1 {
			nearWins = append(nearWins, nearWin{c.Name, metricURL(project, comp, failedKind), failedKind})
		}
		rows = append(rows, row)
	}

	// Worst first, so the top of the map is the top of the to-do list.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Failing > rows[j].Failing })
	shown := min(homeHealthMapCap, len(rows))
	for shown < len(rows) && rows[shown].Failing > 0 {
		shown++
	}
	v.MoreMap = len(rows) - shown
	rows = rows[:shown]
	v.Map = rows

	h := &v.Health
	h.Total = h.Pass + h.Fail + h.None
	h.Pct, h.Tone = "–", "none"
	if measured := h.Pass + h.Fail; measured > 0 {
		pct := int(float64(h.Pass)/float64(measured)*100 + 0.5)
		h.Pct, h.Tone = fmt.Sprintf("%d%%", pct), rateTone(pct)
	}
	if h.Total > 0 {
		// A gap between arcs; a lone full arc needs none.
		share := func(n int) float64 {
			if n == 0 {
				return 0
			}
			l := float64(n) / float64(h.Total) * 100
			if n == h.Total {
				return l
			}
			return math.Max(l-1, 0.5)
		}
		step := func(n int) float64 { return float64(n) / float64(h.Total) * 100 }
		h.PassLen, h.FailLen, h.NoneLen = share(h.Pass), share(h.Fail), share(h.None)
		h.FailOffset = -step(h.Pass)
		h.NoneOffset = -(step(h.Pass) + step(h.Fail))
	}

	// One bar per kind: its average score over every component reporting
	// it, the same number a project's kind tab shows, and how many of
	// those components are below 80.
	for _, r := range rates {
		bar := homeKindBar{Label: r.Label, Info: r.Info, Value: r.Value, Width: r.Pct, Tone: r.Tone, Note: "no data"}
		if r.Score != nil {
			bar.Note = r.Sub
		}
		v.KindBars = append(v.KindBars, bar)
	}

	v.Trends = homeTrends(in)
	v.Insights = homeInsights(totalFails, kindFails, kindProjectFails, projects, func() []homeInsightLink {
		out := []homeInsightLink{}
		for _, n := range nearWins {
			out = append(out, homeInsightLink{n.name + " · " + kindLabels[n.kind], n.href})
		}
		return out
	}(), func() (string, int, bool) {
		if len(tally) < 2 {
			return "", 0, false
		}
		name, worst, found := "", 101, false
		slugs := make([]string, 0, len(tally))
		for s := range tally {
			slugs = append(slugs, s)
		}
		sort.Strings(slugs)
		for _, s := range slugs {
			t := tally[s]
			if t.pass+t.fail == 0 {
				continue
			}
			pct := int(float64(t.pass)/float64(t.pass+t.fail)*100 + 0.5)
			if pct < worst {
				name, worst, found = t.name, pct, true
			}
		}
		return name, worst, found
	}, v.Trends)
	return v
}

// homeInsights turns the tallies into what to do next, most useful first.
// Every one is a fact computed from current data, never a canned tip, and
// one with nothing to say is left out.
func homeInsights(totalFails int, kindFails map[string]int, kindProjectFails map[string]map[string]int,
	projects map[string]*repo.Project, nearWins []homeInsightLink, weakest func() (string, int, bool), trends []homeTrend) []homeInsight {
	var out []homeInsight
	if totalFails == 0 {
		return []homeInsight{{Figure: "0", Tone: "pass", Title: "No check is failing", Detail: "Every connected integration passes on every component."}}
	}

	// The integration behind the most failures.
	topKind, topN := "", 0
	for _, k := range bindableKinds {
		if kindFails[k] > topN {
			topKind, topN = k, kindFails[k]
		}
	}
	biggest := homeInsight{Figure: fmt.Sprintf("%d/%d", topN, totalFails), Tone: "fail",
		Title:  kindLabels[topKind] + " causes the most failures",
		Detail: fmt.Sprintf("%d of %d failing checks come from it. A shared fix here moves the score furthest.", topN, totalFails)}
	slugs := make([]string, 0, len(kindProjectFails[topKind]))
	for s := range kindProjectFails[topKind] {
		slugs = append(slugs, s)
	}
	sort.Slice(slugs, func(i, j int) bool {
		a, b := kindProjectFails[topKind][slugs[i]], kindProjectFails[topKind][slugs[j]]
		return a > b || (a == b && slugs[i] < slugs[j])
	})
	if len(slugs) > 0 {
		biggest.Links = []homeInsightLink{{"Open " + kindLabels[topKind], projectMetricURL(projects[slugs[0]], topKind)}}
	}
	out = append(out, biggest)

	// Components one fix away from green.
	if n := len(nearWins); n > 0 {
		links := nearWins
		if n > homeNearWinNames {
			links = nearWins[:homeNearWinNames]
		}
		out = append(out, homeInsight{Figure: strconv.Itoa(n), Tone: "pass",
			Title:  fmt.Sprintf("%d component%s one fix away from green", n, plural(n, " is", "s are")),
			Detail: "Each fails a single integration: the cheapest wins on the board.", Links: links})
	}

	if name, pct, ok := weakest(); ok {
		out = append(out, homeInsight{Figure: fmt.Sprintf("%d%%", pct), Tone: rateTone(pct),
			Title: name + " is the weakest project", Detail: fmt.Sprintf("Only %d%% of its checks pass.", pct)})
	}

	// The trend that got worst, relative to where it started.
	var worst *homeTrend
	for i := range trends {
		if t := &trends[i]; t.DeltaTone == "fail" && (worst == nil || t.change > worst.change) {
			worst = t
		}
	}
	if worst != nil {
		out = append(out, homeInsight{Figure: worst.Delta, Tone: "fail",
			Title:  capitalize(strings.ToLower(worst.Label)) + " got worse",
			Detail: fmt.Sprintf("Over the last %d days. Catch it before it reaches a quality gate.", homeTrendDays)})
	}
	return out
}

// homeTrends renders one chart per integration that has data, and the
// change across the window.
func homeTrends(in homeInputs) []homeTrend {
	var out []homeTrend
	for _, kind := range bindableKinds {
		key := primaryChartKey(kind)
		points := in.Trends[key]
		meta := in.TrendKeys[key]
		chart := buildDailyChartSVG(points, meta.Direction, meta.Unit, 560, 130)
		t := homeTrend{Label: homeTrendDefs[kind].Label, Chart: chart, DeltaTone: "none"}
		first, last := -1, -1
		for i, p := range points {
			if p.HasValue {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first >= 0 {
			delta := points[last].Value - points[first].Value
			// Judged on what is shown: a change that rounds to nothing is none.
			shown := formatMetricValue(delta, meta.Unit)
			if strings.Trim(shown, "-0.%") == "" {
				t.Delta = "no change"
			} else {
				if delta > 0 {
					shown = "+" + shown
				}
				t.Delta, t.DeltaTone = shown, chart.Tone
				t.change = math.Abs(delta)
				if start := math.Abs(points[first].Value); start > 0 {
					t.change /= start
				}
			}
		}
		out = append(out, t)
	}
	return out
}
