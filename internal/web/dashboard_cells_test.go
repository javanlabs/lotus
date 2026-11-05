package web

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
)

// cellFor is row's cell in kind's column, looked up through cols so the
// test follows the column order rather than hard-coding positions.
func cellFor(t *testing.T, cols []dashboardColumn, row dashboardRow, kind string) badgeCell {
	t.Helper()
	for i, col := range cols {
		if col.Key == kind {
			return row.Cells[i]
		}
	}
	t.Fatalf("no column %q in %+v", kind, cols)
	return badgeCell{}
}

// On a project page each metric cell opens the component's tab for its
// kind; a kind the component does not report stays plain, since its tab
// would be a 404.
func TestProjectPageCells_LinkToTheComponentMetricTab(t *testing.T) {
	t.Parallel()
	projectID, compID := uuid.New(), uuid.New()
	comp := componentInfo{ID: compID, Name: "app-react-v2", Slug: "app-react-v2",
		ProjectID: projectID, ProjectName: "AlurKerja", ProjectSlug: "alurkerja"}
	metrics := map[uuid.UUID]repo.ComponentMetric{
		compID: {ComponentID: compID, Status: "fail", MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sonarqube": {Status: "pass", Score: intPtr(88)},
			"sentry":    {Status: "fail", Score: intPtr(42)},
			"gitlab":    {Status: "pass", CountValue: int64Ptr(5), CountLabel: "stale branch"},
		})},
	}

	vm := buildDashboardViewModel("", 1, []componentInfo{comp}, metrics)
	row := vm.Rows[0]

	if got, want := cellFor(t, vm.Columns, row, "sentry").Href, "/projects/alurkerja/components/app-react-v2/metrics/sentry"; got != want {
		t.Fatalf("sentry cell Href = %q, want %q", got, want)
	}
	project := &repo.Project{ID: projectID, Slug: "alurkerja"}
	component := &repo.Component{ID: compID, Slug: "app-react-v2"}
	for _, k := range []string{"sonarqube", "sentry", "gitlab"} {
		c := cellFor(t, vm.Columns, row, k)
		if want := metricURL(project, component, k); c.Href != want {
			t.Errorf("%s cell Href = %q, want %q", k, c.Href, want)
		}
		if !strings.Contains(c.LinkLabel, "app-react-v2") || !strings.Contains(c.LinkLabel, kindLabels[k]) || !strings.Contains(c.LinkLabel, c.Text) {
			t.Errorf("%s cell LinkLabel = %q, want the component, the integration and the value", k, c.LinkLabel)
		}
	}
	if c := cellFor(t, vm.Columns, row, "trivy"); c.Href != "" || c.LinkLabel != "" {
		t.Fatalf("unbound trivy cell links: %+v", c)
	}
	// The row itself still opens the component.
	if want := componentURL(project, component); row.Href != want {
		t.Fatalf("row Href = %q, want %q", row.Href, want)
	}
}

// A component that has never synced has no cell to open.
func TestProjectPageCells_NeverSyncedComponentHasNoLinks(t *testing.T) {
	t.Parallel()
	compID := uuid.New()
	vm := buildDashboardViewModel("", 1, []componentInfo{{ID: compID, Name: "new", Slug: "new", ProjectSlug: "p"}}, nil)
	for i, c := range vm.Rows[0].Cells {
		if c.Href != "" {
			t.Fatalf("cell %s of a never-synced component links to %q", vm.Columns[i].Key, c.Href)
		}
	}
}

// A component whose slug cannot be routed is linked by id, as its own page
// is — the cell goes through metricURL, not string building.
func TestProjectPageCells_UnroutableSlugFallsBackToID(t *testing.T) {
	t.Parallel()
	projectID, compID := uuid.New(), uuid.New()
	comp := componentInfo{ID: compID, Name: "New", Slug: "new", ProjectID: projectID, ProjectSlug: "p"}
	metrics := map[uuid.UUID]repo.ComponentMetric{
		compID: {ComponentID: compID, MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{"trivy": {Status: "pass"}})},
	}
	vm := buildDashboardViewModel("", 1, []componentInfo{comp}, metrics)
	want := "/projects/p/components/" + compID.String() + "/metrics/trivy"
	if got := cellFor(t, vm.Columns, vm.Rows[0], "trivy").Href; got != want {
		t.Fatalf("trivy cell Href = %q, want %q", got, want)
	}
}

// On the organization's project list a cell is the project's rollup for
// one kind, so it opens the project's page for that kind.
func TestProjectListCells_LinkToTheProjectMetricPage(t *testing.T) {
	t.Parallel()
	projectID, compID := uuid.New(), uuid.New()
	comps := []componentInfo{{ID: compID, Name: "app-react-v2", Slug: "app-react-v2",
		ProjectID: projectID, ProjectName: "AlurKerja", ProjectSlug: "alurkerja"}}
	metrics := map[uuid.UUID]repo.ComponentMetric{
		compID: {ProjectID: projectID, ComponentID: compID, Status: "pass", MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sentry": {Status: "pass", Score: intPtr(91)},
		})},
	}
	projects := []repo.Project{{ID: projectID, Name: "AlurKerja", Slug: "alurkerja"}}

	vm := buildProjectDashboardViewModel("", projects, nil, comps, metrics)
	row := vm.Rows[0]
	if got, want := cellFor(t, vm.Columns, row, "sentry").Href, projectMetricURL(&projects[0], "sentry"); got != want || want != "/projects/alurkerja/metrics/sentry" {
		t.Fatalf("sentry cell Href = %q, want %q", got, want)
	}
	for _, k := range []string{"sonarqube", "trivy", "gitlab"} {
		if c := cellFor(t, vm.Columns, row, k); c.Href != "" {
			t.Errorf("%s cell has no data but links to %q", k, c.Href)
		}
	}
}

var anchorTag = regexp.MustCompile(`<a\b[^>]*>|</a>`)

// The rendered table: a row is not a link (its cells are, and links
// cannot nest), its name is, and a linked cell is one <a> carrying the
// cell's href while an empty cell is a plain badge.
func TestDashboardBlock_RowsHoldCellLinksWithoutNesting(t *testing.T) {
	t.Parallel()
	projectID, compID := uuid.New(), uuid.New()
	comp := componentInfo{ID: compID, Name: "app-react-v2", Slug: "app-react-v2",
		ProjectID: projectID, ProjectName: "AlurKerja", ProjectSlug: "alurkerja"}
	metrics := map[uuid.UUID]repo.ComponentMetric{
		// Synced just now, so the cell is not drawn stale.
		compID: {ComponentID: compID, Status: "pass", UpdatedAt: time.Now(), MetricsJSON: metricsJSON(t, map[string]repo.KindHeadline{
			"sentry": {Status: "pass", Score: intPtr(84)},
		})},
	}
	vm := buildDashboardViewModel("", 1, []componentInfo{comp}, metrics)

	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	var out bytes.Buffer
	if err := r.RenderNamed(&out, "dashboard_block", vm); err != nil {
		t.Fatal(err)
	}
	html := out.String()

	depth := 0
	for _, tag := range anchorTag.FindAllString(html, -1) {
		if tag == "</a>" {
			depth--
			continue
		}
		depth++
		if depth > 1 {
			t.Fatalf("nested anchor: %s", tag)
		}
	}
	if depth != 0 {
		t.Fatalf("unbalanced anchors (depth %d)", depth)
	}

	if strings.Contains(html, `<a href="/projects/alurkerja/components/app-react-v2" class="lotus-repo-row`) {
		t.Fatal("the row is still an <a>")
	}
	if !strings.Contains(html, `<a href="/projects/alurkerja/components/app-react-v2" class="lotus-row-link"`) {
		t.Fatal("the row's name is not its link")
	}
	cell := regexp.MustCompile(`<a href="/projects/alurkerja/components/app-react-v2/metrics/sentry" class="lotus-badge lotus-badge-pass lotus-badge-link"[^>]*>84</a>`)
	if !cell.MatchString(html) {
		t.Fatalf("sentry cell is not a link to its metric tab:\n%s", html)
	}
	if !strings.Contains(html, `aria-label="app-react-v2, Sentry: 84"`) {
		t.Fatal("sentry cell link has no accessible name")
	}
	if n := strings.Count(html, `<span class="lotus-badge lotus-badge-none">–</span>`); n != len(bindableKinds)-1 {
		t.Fatalf("plain empty cells = %d, want %d", n, len(bindableKinds)-1)
	}
}
