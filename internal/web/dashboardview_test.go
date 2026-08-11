package web

import (
	"encoding/json"
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

func TestBuildDashboardViewModel_RowsCarryBadgesAndSortByScoreAscending(t *testing.T) {
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
	// Sorted ascending by score: the fail-status, lower-score component
	// (risky-svc, 40) must lead — "sorted by score ▲" surfaces the most
	// urgent repository first.
	if vm.Rows[0].Name != "risky-svc" || vm.Rows[0].Score != "40" || vm.Rows[0].Tone != "fail" {
		t.Fatalf("Rows[0] = %+v, want risky-svc/40/fail first", vm.Rows[0])
	}
	if vm.Rows[1].Name != "healthy-svc" || vm.Rows[1].Score != "90" || vm.Rows[1].Tone != "pass" {
		t.Fatalf("Rows[1] = %+v, want healthy-svc/90/pass second", vm.Rows[1])
	}
	if vm.Rows[0].Trivy.Text != "3 critical findings" || vm.Rows[0].Trivy.Tone != "fail" {
		t.Fatalf("Rows[0].Trivy = %+v, want '3 critical findings'/fail", vm.Rows[0].Trivy)
	}
	// gitlab/sonarqube/sentry were never bound on risky-svc — must render
	// as the "no data" badge, not a zero value or a panic.
	if vm.Rows[0].GitLab.Tone != "none" || vm.Rows[0].GitLab.Text != "–" {
		t.Fatalf("Rows[0].GitLab = %+v, want no-data placeholder", vm.Rows[0].GitLab)
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

func TestBuildScoreCard_BucketsByStatusExcludingUnknownFromBar(t *testing.T) {
	scores := []*int{intPtr(80), intPtr(40)}
	statuses := statusList("pass", "fail", "unknown")
	sc := buildScoreCard(statuses, scores)
	if sc.HealthyN != 1 || sc.FailingN != 1 {
		t.Fatalf("HealthyN=%d FailingN=%d, want 1/1", sc.HealthyN, sc.FailingN)
	}
	if sc.HealthyPct+sc.AtRiskPct+sc.FailingPct != 100 {
		t.Fatalf("segments sum to %d, want 100 (unknown excluded from the denominator)", sc.HealthyPct+sc.AtRiskPct+sc.FailingPct)
	}
	if !sc.HasScore || sc.Score != 60 {
		t.Fatalf("Score = %+v, want HasScore=true Score=60 (avg of 80,40)", sc)
	}
}

func TestBuildKPIs_NoDataYetWhenKindNeverBound(t *testing.T) {
	kindStatuses := map[string][]collector.Status{
		"sonarqube": statusList("pass", "pass", "warn"),
		// trivy/sentry/gitlab: absent entirely — never bound anywhere.
	}
	kpis := buildKPIs(kindStatuses, map[string]int{"sonarqube": 3}, 5)

	byLabel := map[string]kpiCard{}
	for _, k := range kpis {
		byLabel[k.Label] = k
	}
	sonar := byLabel["SONAR GATE"]
	if sonar.Value != "67" || sonar.Tone != "warn" {
		t.Fatalf("SONAR GATE = %+v, want value=67 tone=warn (2/3 pass, rounded)", sonar)
	}
	trivy := byLabel["TRIVY CLEAN"]
	if trivy.Value != "–" || trivy.Tone != "none" || trivy.Sub != "no data yet" {
		t.Fatalf("TRIVY CLEAN = %+v, want the no-data placeholder", trivy)
	}
}
