package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func intPtr(v int) *int { return &v }

func int64Ptr(v int64) *int64 { return &v }

func statusList(statuses ...collector.Status) []collector.Status { return statuses }

// projectsOf derives the project list a set of components belongs to.
func projectsOf(comps []componentInfo) []repo.Project {
	seen := map[uuid.UUID]bool{}
	var out []repo.Project
	for _, c := range comps {
		if !seen[c.ProjectID] {
			seen[c.ProjectID] = true
			out = append(out, repo.Project{ID: c.ProjectID, Name: c.ProjectName})
		}
	}
	return out
}

func TestBuildDashboardViewModel_EmptyOrgHasNoComponents(t *testing.T) {
	t.Parallel()
	vm := buildDashboardViewModel("subtitle", 0, nil, nil)
	if !vm.Empty || !vm.NoProjects {
		t.Fatalf("Empty=%v NoProjects=%v, want both true for zero projects", vm.Empty, vm.NoProjects)
	}
}

// TestBuildDashboardViewModel_ProjectWithNoComponentsIsNotNoProjects covers
// a real bug caught in manual QA: a project can exist with zero components
// (no repositories added to it yet) — the table is still empty, but the
// copy must say "add a repository", not "create your first project" (the
// project already exists).
func TestBuildDashboardViewModel_ProjectWithNoComponentsIsNotNoProjects(t *testing.T) {
	t.Parallel()
	vm := buildDashboardViewModel("subtitle", 1, nil, nil)
	if !vm.Empty {
		t.Fatalf("Empty = false, want true (no components to show)")
	}
	if vm.NoProjects {
		t.Fatalf("NoProjects = true, want false — a project exists, it just has no repositories yet")
	}
	if vm.ProjectCount != 1 {
		t.Fatalf("ProjectCount = %d, want 1", vm.ProjectCount)
	}
}

func metricsJSON(t *testing.T, kinds map[string]repo.KindHeadline) []byte {
	t.Helper()
	b, err := json.Marshal(kinds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestBuildDashboardViewModel_RowsCarryBadgesAndSortAlphabetically(t *testing.T) {
	t.Parallel()
	compA, compB := uuid.New(), uuid.New()
	scoreA, scoreB := 90, 40
	components := []componentInfo{
		{ID: compA, Name: "healthy-svc", ProjectName: "Platform"},
		{ID: compB, Name: "risky-svc", ProjectName: "Platform"},
	}
	metrics := map[uuid.UUID]repo.ComponentMetric{
		compA: {
			ComponentID: compA, Status: "pass", Score: &scoreA,
			MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
				"gitlab": {Status: "pass"},
			}),
		},
		compB: {
			ComponentID: compB, Status: "fail", Score: &scoreB,
			MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
				"trivy": {Status: "fail", Score: intPtr(49), CountValue: int64Ptr(3), CountLabel: "critical finding"},
			}),
		},
	}

	vm := buildDashboardViewModel("2 repos", 1, components, metrics)
	if vm.Empty {
		t.Fatal("Empty = true, want false")
	}
	if len(vm.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(vm.Rows))
	}
	// Default order is alphabetical, not by score: a row should stay where
	// the reader last saw it when the numbers move underneath. Sorting by
	// score is a header click away, client-side.
	if vm.Rows[0].Name != "healthy-svc" || vm.Rows[1].Name != "risky-svc" {
		t.Fatalf("order = %q, %q; want alphabetical (healthy-svc, risky-svc)",
			vm.Rows[0].Name, vm.Rows[1].Name)
	}
	// ScoreValue backs client-side numeric sorting; without it the client
	// would have to re-parse "–" as a number.
	if vm.Rows[1].ScoreValue == nil || *vm.Rows[1].ScoreValue != 40 {
		t.Fatalf("risky-svc ScoreValue = %v, want 40", vm.Rows[1].ScoreValue)
	}
	// Cells are positional against vm.Columns, so assert through the column
	// list rather than fixed field names — that is what keeps this test
	// honest when a column is added or reordered.
	cell := func(row dashboardRow, key string) badgeCell {
		for i, col := range vm.Columns {
			if col.Key == key {
				return row.Cells[i]
			}
		}
		t.Fatalf("no column with key %q in %+v", key, vm.Columns)
		return badgeCell{}
	}
	// The cell is the score, coloured by its band; the collector's own
	// phrasing is the hover text that explains it.
	if got := cell(vm.Rows[1], "trivy"); got.Text != "49" || got.Tone != "fail" || got.Title != "3 critical findings" {
		t.Fatalf("risky-svc trivy cell = %+v, want 49/fail titled '3 critical findings'", got)
	}
	// A kind with no score is a grey "–" whatever its stored status, and
	// the row's tone is its score's band (40: needs attention).
	if got := cell(vm.Rows[0], "gitlab"); got.Text != "–" || got.Tone != "none" || got.Title != "No data" {
		t.Fatalf("healthy-svc gitlab cell = %+v, want grey – titled No data", got)
	}
	if vm.Rows[0].Tone != "pass" || vm.Rows[1].Tone != "fail" {
		t.Fatalf("row tones = %q, %q; want the bands of 90 and 40", vm.Rows[0].Tone, vm.Rows[1].Tone)
	}
	// gitlab/sonarqube/sentry were never bound on risky-svc — must render
	// as the "no data" badge, not a zero value or a panic.
	if got := cell(vm.Rows[1], "gitlab"); got.Tone != "none" || got.Text != "–" {
		t.Fatalf("risky-svc gitlab cell = %+v, want no-data placeholder", got)
	}
	if len(vm.Rows[1].Cells) != len(vm.Columns) {
		t.Fatalf("row has %d cells but %d columns — the table would misalign",
			len(vm.Rows[1].Cells), len(vm.Columns))
	}
}

// TestHeadlineText_DoesNotDoublePluralizeAnAlreadyPluralLabel is a real
// bug caught in manual QA: sonarqube's CountLabel is "bugs +
// vulnerabilities" (already plural) — naively appending "s" for any
// count != 1 produced "vulnerabilitiess".
func TestHeadlineText_DoesNotDoublePluralizeAnAlreadyPluralLabel(t *testing.T) {
	t.Parallel()
	got := headlineText(repo.KindHeadline{CountValue: int64Ptr(3), CountLabel: "bugs + vulnerabilities"})
	if got != "3 bugs + vulnerabilities" {
		t.Fatalf("headlineText = %q, want %q", got, "3 bugs + vulnerabilities")
	}
}

func TestHeadlineText_PluralizesASingularLabel(t *testing.T) {
	t.Parallel()
	got := headlineText(repo.KindHeadline{CountValue: int64Ptr(2), CountLabel: "critical finding"})
	if got != "2 critical findings" {
		t.Fatalf("headlineText = %q, want %q", got, "2 critical findings")
	}
	got = headlineText(repo.KindHeadline{CountValue: int64Ptr(1), CountLabel: "critical finding"})
	if got != "1 critical finding" {
		t.Fatalf("headlineText = %q, want %q (singular count stays singular)", got, "1 critical finding")
	}
	// gitlab's real CountLabel — "branch" ends in "ch", a sibilant that
	// takes "es" not "s" ("branchs" is not a word).
	got = headlineText(repo.KindHeadline{CountValue: int64Ptr(5), CountLabel: "stale branch"})
	if got != "5 stale branches" {
		t.Fatalf("headlineText = %q, want %q", got, "5 stale branches")
	}
}

func TestBuildDashboardViewModel_ComponentNeverCollectedIsUnknownNotPass(t *testing.T) {
	t.Parallel()
	comp := uuid.New()
	components := []componentInfo{{ID: comp, Name: "never-synced", ProjectName: "P"}}
	// No entry in metrics at all — never had a successful Collect.
	vm := buildDashboardViewModel("1 repo", 1, components, map[uuid.UUID]repo.ComponentMetric{})
	if vm.Rows[0].Tone != "none" {
		t.Fatalf("Tone = %q, want %q (never-collected must not read as pass)", vm.Rows[0].Tone, "none")
	}
	if vm.Rows[0].Score != "–" {
		t.Fatalf("Score = %q, want %q", vm.Rows[0].Score, "–")
	}
}

// The score tile reads a rollup: its number, the band of that number, and
// the count of components needing attention the average can hide.
func TestBuildScoreCard_BandAndAttentionCount(t *testing.T) {
	t.Parallel()
	sc := buildScoreCard(repo.ProjectRollup{Score: intPtr(84), Components: 5, NeedAttention: 2})
	if !sc.HasScore || sc.Score != 84 || sc.Tone != "pass" || sc.Word != "Pass" {
		t.Fatalf("score card = %+v, want 84 Pass", sc)
	}
	if got := attentionText(sc.NeedAttention, sc.Components); got != "2 of 5 components fail" {
		t.Fatalf("attention = %q", got)
	}
	if got := attentionText(0, 1); got != "0 of 1 component fails" {
		t.Fatalf("attention = %q", got)
	}
	none := buildScoreCard(repo.ProjectRollup{})
	if none.HasScore || none.Tone != "none" {
		t.Fatalf("empty score card = %+v, want no score, grey", none)
	}
}

// The KPI tabs are per-kind average scores, not pass rates: the same
// numbers the project score is built from.
func TestBuildKPIs_AverageScoresNotPassRates(t *testing.T) {
	t.Parallel()
	kpis := buildKPIs(map[string]repo.KindRollup{
		"sonarqube": {Score: intPtr(77), Scored: 3, Below: 1, Count: 3},
		"gitlab":    {Score: intPtr(88), Scored: 2, Below: 0, Count: 2},
		// trivy/sentry: absent entirely — never bound anywhere.
	})
	byKey := map[string]kpiCard{}
	for _, k := range kpis {
		byKey[k.Key] = k
	}
	sonar := byKey["sonarqube"]
	if sonar.Label != "SONARQUBE" || sonar.Value != "77" || sonar.Unit != "/100" || sonar.Tone != "fail" || sonar.Sub != "1 of 3 below 80" {
		t.Fatalf("sonarqube = %+v, want SONARQUBE 77/100 fail, 1 of 3 below 80", sonar)
	}
	gitlab := byKey["gitlab"]
	if gitlab.Label != "GITLAB" || gitlab.Value != "88" || gitlab.Tone != "pass" || gitlab.Sub != "0 of 2 below 80" {
		t.Fatalf("gitlab = %+v, want GITLAB 88 pass", gitlab)
	}
	trivy := byKey["trivy"]
	if trivy.Value != "–" || trivy.Tone != "none" || trivy.Sub != "no data yet" {
		t.Fatalf("trivy = %+v, want the no-data placeholder", trivy)
	}
}

// TestProjectScore_IsIdenticalOnBothPages pins the bug this test was written
// after: the organization dashboard rolled a project up per-kind first,
// while the project's own page flat-averaged its component scores. The same
// project read 81 in the list and 80 on its own page — two numbers, both
// called "the Lotus score".
func TestProjectScore_IsIdenticalOnBothPages(t *testing.T) {
	t.Parallel()
	// Deliberately lopsided: two components report gitlab, one reports
	// sonarqube. A flat average and a per-kind average disagree here, which
	// is exactly what made the discrepancy visible.
	metrics := map[uuid.UUID]repo.ComponentMetric{}
	var comps []componentInfo
	projectID := uuid.New()
	for i, kinds := range []map[string]repo.KindHeadline{
		{"gitlab": {Status: "pass", Score: intPtr(90)}},
		{"gitlab": {Status: "pass", Score: intPtr(80)}},
		{"sonarqube": {Status: "fail", Score: intPtr(30)}},
	} {
		id := uuid.New()
		b, err := json.Marshal(kinds)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		score := 0
		for _, kh := range kinds {
			score = *kh.Score
		}
		metrics[id] = repo.ComponentMetric{
			ProjectID: projectID, ComponentID: id, MetricsJSON: b, Score: &score, Status: "pass",
		}
		comps = append(comps, componentInfo{
			ID: id, Name: fmt.Sprintf("c%d", i), ProjectID: projectID, ProjectName: "Payments",
		})
	}

	onProjectPage := buildDashboardViewModel("", 1, comps, metrics)
	onDashboard := buildProjectDashboardViewModel("", projectsOf(comps), nil, comps, metrics)

	if !onProjectPage.Score.HasScore || len(onDashboard.Rows) != 1 {
		t.Fatalf("fixture did not produce a scored project: card=%+v rows=%d",
			onProjectPage.Score, len(onDashboard.Rows))
	}
	want := onDashboard.Rows[0].Score
	got := fmt.Sprintf("%d", onProjectPage.Score.Score)
	if got != want {
		t.Fatalf("project page tile shows %s but the dashboard row shows %s — one project must have one Lotus score", got, want)
	}
}

// TestKindOrderIsConsistentEverywhere enforces AGENTS.md's rule: the four
// integrations appear in one fixed order wherever they are listed. The
// table columns derive from bindableKinds, but kpiDefs carries its own
// per-card copy — so this is what stops the KPI strip drifting out of step
// with the columns beneath it, which is exactly the inconsistency the
// convention exists to prevent.
func TestKindOrderIsConsistentEverywhere(t *testing.T) {
	t.Parallel()
	want := []string{"sonarqube", "trivy", "sentry", "gitlab"}

	if len(bindableKinds) != len(want) {
		t.Fatalf("bindableKinds = %v, want %v", bindableKinds, want)
	}
	for i, k := range want {
		if bindableKinds[i] != k {
			t.Fatalf("bindableKinds = %v, want %v (see AGENTS.md)", bindableKinds, want)
		}
	}

	var kpiOrder []string
	for _, d := range kpiDefs {
		kpiOrder = append(kpiOrder, d.Kind)
	}
	for i, k := range want {
		if kpiOrder[i] != k {
			t.Fatalf("kpiDefs order = %v, want %v (see AGENTS.md)", kpiOrder, want)
		}
	}

	for i, k := range want {
		if projectColumns[i].Key != k {
			t.Fatalf("projectColumns order = %+v, want %v (see AGENTS.md)", projectColumns, want)
		}
		if componentColumns[i].Key != k {
			t.Fatalf("componentColumns order = %+v, want %v (see AGENTS.md)", componentColumns, want)
		}
	}

	// Every kind needs a header heading, or a column renders blank.
	for _, k := range bindableKinds {
		if columnHeadings[k] == "" {
			t.Fatalf("columnHeadings has no entry for %q", k)
		}
		if kindLabels[k] == "" {
			t.Fatalf("kindLabels has no entry for %q", k)
		}
	}
}

func TestStaleCells_MarkEachIntegrationNotSyncedForADay(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	dashboardNow = func() time.Time { return now }
	t.Cleanup(func() { dashboardNow = time.Now })

	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	comp := uuid.New()
	cm := repo.ComponentMetric{
		ComponentID: comp, Status: "pass", UpdatedAt: now.Add(-10 * time.Minute),
		MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"gitlab":    {Status: "pass", UpdatedAt: at(10 * time.Minute)},
			"sonarqube": {Status: "pass", UpdatedAt: at(3 * 24 * time.Hour)},
			"sentry":    {Status: "pass", UpdatedAt: at(23 * time.Hour)},
			"trivy":     {Status: "fail", UpdatedAt: at(30 * time.Hour)},
		}),
	}
	vm := buildDashboardViewModel("1 repo", 1, []componentInfo{{ID: comp, Name: "api"}}, map[uuid.UUID]repo.ComponentMetric{comp: cm})
	if !vm.ShowUpdated {
		t.Fatal("project page has no last-updated column")
	}
	row := vm.Rows[0]
	if row.Updated == nil || row.Updated.Text != "10 min ago" || row.Updated.Sort == nil {
		t.Fatalf("updated cell = %+v, want '10 min ago' with a sort key", row.Updated)
	}
	stale := map[string]string{}
	for i, col := range vm.Columns {
		stale[col.Key] = row.Cells[i].Stale
	}
	// Both out-of-date integrations are marked, each on its own cell.
	if !strings.Contains(stale["sonarqube"], "SonarQube has not synced") || !strings.Contains(stale["sonarqube"], "Last synced 3 days ago") {
		t.Fatalf("sonarqube cell note:\n%s", stale["sonarqube"])
	}
	if !strings.Contains(stale["trivy"], "Trivy has not synced") || !strings.Contains(stale["trivy"], "Last synced 1 day ago") {
		t.Fatalf("trivy cell note:\n%s", stale["trivy"])
	}
	if stale["gitlab"] != "" || stale["sentry"] != "" {
		t.Fatalf("fresh cells marked stale: gitlab=%q sentry=%q", stale["gitlab"], stale["sentry"])
	}

	// Never synced: a dash and no sort key.
	if c := updatedCellFor(repo.ComponentMetric{}, false); c.Text != "–" || c.Sort != nil {
		t.Fatalf("never-synced cell = %+v", c)
	}
	// A kind written before per-kind times falls back to the row's time.
	cm.UpdatedAt = now.Add(-30 * time.Hour)
	if got := kindSyncedAt(cm, repo.KindHeadline{Status: "pass"}); !got.Equal(cm.UpdatedAt) {
		t.Fatalf("legacy kind synced at %v, want the row's %v", got, cm.UpdatedAt)
	}
}

// The integration columns are the four collectors and nothing else: PERF
// and SECURITY were dropped, and every row has one cell per column plus
// the last-updated cell.
func TestComponentColumns_NoPlaceholderColumns(t *testing.T) {
	t.Parallel()
	for _, c := range componentColumns {
		if c.Key == "" {
			t.Fatalf("placeholder column %q is back", c.Label)
		}
	}
}
