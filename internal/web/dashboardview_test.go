package web

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func intPtr(v int) *int { return &v }

func int64Ptr(v int64) *int64 { return &v }

func statusList(statuses ...collector.Status) []collector.Status { return statuses }

func TestBuildDashboardViewModel_EmptyOrgHasNoComponents(t *testing.T) {
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
				"trivy": {Status: "fail", CountValue: int64Ptr(3), CountLabel: "critical finding"},
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
	// risky-svc's trivy headline carries a count but no score, so the cell
	// falls back to the collector's own phrasing rather than showing "–".
	if got := cell(vm.Rows[1], "trivy"); got.Text != "3 critical findings" || got.Tone != "fail" {
		t.Fatalf("risky-svc trivy cell = %+v, want '3 critical findings'/fail", got)
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
	got := headlineText(repo.KindHeadline{CountValue: int64Ptr(3), CountLabel: "bugs + vulnerabilities"})
	if got != "3 bugs + vulnerabilities" {
		t.Fatalf("headlineText = %q, want %q", got, "3 bugs + vulnerabilities")
	}
}

func TestHeadlineText_PluralizesASingularLabel(t *testing.T) {
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

// buildScoreCard now takes the already-rolled-up score rather than a slice
// to average, because the two levels roll up differently and the tile must
// show whichever number its page's rows show.
func TestBuildScoreCard_BucketsByStatusExcludingUnknownFromBar(t *testing.T) {
	statuses := statusList("pass", "fail", "unknown")
	sc := buildScoreCard(statuses, intPtr(60))
	if sc.HealthyN != 1 || sc.FailingN != 1 {
		t.Fatalf("HealthyN=%d FailingN=%d, want 1/1", sc.HealthyN, sc.FailingN)
	}
	if sc.HealthyPct+sc.FailingPct != 100 {
		t.Fatalf("segments sum to %d, want 100 (unknown excluded from the denominator)", sc.HealthyPct+sc.FailingPct)
	}
	if !sc.HasScore || sc.Score != 60 {
		t.Fatalf("Score = %+v, want HasScore=true Score=60 (the score it was handed)", sc)
	}
}

func TestBuildKPIs_NoDataYetWhenKindNeverBound(t *testing.T) {
	kindStatuses := map[string][]collector.Status{
		"sonarqube": statusList("pass", "pass", "fail"),
		// trivy/sentry/gitlab: absent entirely — never bound anywhere.
	}
	kpis := buildKPIs(kindStatuses, map[string]int{"sonarqube": 3}, 5)

	byLabel := map[string]kpiCard{}
	for _, k := range kpis {
		byLabel[k.Label] = k
	}
	sonar := byLabel["SONAR GATE"]
	if sonar.Value != "67" || sonar.Tone != "fail" {
		t.Fatalf("SONAR GATE = %+v, want value=67 tone=fail (2/3 pass, rounded — below the 80%% pass bar)", sonar)
	}
	trivy := byLabel["TRIVY CLEAN"]
	if trivy.Value != "–" || trivy.Tone != "none" || trivy.Sub != "no data yet" {
		t.Fatalf("TRIVY CLEAN = %+v, want the no-data placeholder", trivy)
	}
}

// TestProjectScore_IsIdenticalOnBothPages pins the bug this test was written
// after: the organization dashboard rolled a project up per-kind first,
// while the project's own page flat-averaged its component scores. The same
// project read 81 in the list and 80 on its own page — two numbers, both
// called "the Lotus score".
func TestProjectScore_IsIdenticalOnBothPages(t *testing.T) {
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
	onDashboard := buildProjectDashboardViewModel("", projectsOf(comps), nil, comps, metrics, nil)

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
