package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/repo"
)

// treeFixture builds buildProjectDashboardViewModel's inputs one project at
// a time, so each test reads as the tree it describes.
type treeFixture struct {
	t          *testing.T
	folders    []repo.Folder
	projects   []repo.Project
	components []componentInfo
	metrics    map[uuid.UUID]repo.ComponentMetric
}

func newTreeFixture(t *testing.T) *treeFixture {
	return &treeFixture{t: t, metrics: map[uuid.UUID]repo.ComponentMetric{}}
}

func (f *treeFixture) folder(name string, parent *repo.Folder) repo.Folder {
	fo := repo.Folder{ID: uuid.New(), Name: name}
	if parent != nil {
		pid := parent.ID
		fo.ParentID = &pid
	}
	f.folders = append(f.folders, fo)
	return fo
}

// project adds a project with one component whose SonarQube headline has
// the given status and score. status "" adds a project with no components;
// "unmeasured" adds one whose component has never reported.
func (f *treeFixture) project(name string, in *repo.Folder, status string, score *int) uuid.UUID {
	f.t.Helper()
	p := repo.Project{ID: uuid.New(), Name: name}
	if in != nil {
		fid := in.ID
		p.FolderID = &fid
	}
	f.projects = append(f.projects, p)
	if status == "" {
		return p.ID
	}
	c := componentInfo{ID: uuid.New(), Name: name + "-api", ProjectID: p.ID, ProjectName: name}
	f.components = append(f.components, c)
	if status == "unmeasured" {
		return p.ID
	}
	f.metrics[c.ID] = repo.ComponentMetric{
		ProjectID: p.ID, ComponentID: c.ID, Status: status, Score: score,
		MetricsJSON: metricsJSON(f.t, map[string]repo.KindHeadline{"sonarqube": {Status: status, Score: score}}),
	}
	return p.ID
}

func (f *treeFixture) build() dashboardViewModel {
	return buildProjectDashboardViewModel("", f.projects, f.folders, f.components, f.metrics)
}

func rowNames(vm dashboardViewModel) []string {
	out := make([]string, 0, len(vm.Rows))
	for _, r := range vm.Rows {
		out = append(out, r.Name)
	}
	return out
}

func rowNamed(t *testing.T, vm dashboardViewModel, name string) dashboardRow {
	t.Helper()
	for _, r := range vm.Rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q in %v", name, rowNames(vm))
	return dashboardRow{}
}

// projectsOf derives the project list a set of components belongs to, for
// tests written before buildProjectDashboardViewModel took projects.
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

func TestProjectTree_FoldersFirstThenProjectsDepthFirst(t *testing.T) {
	f := newTreeFixture(t)
	product := f.folder("Product", nil)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Unfiled", nil, "pass", intPtr(90))
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("AlurKerja", &product, "pass", intPtr(84))
	vm := f.build()

	want := []string{"IT Services", "Tribe lead A", "Linknet", "Product", "AlurKerja", "Unfiled"}
	if got := rowNames(vm); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !vm.Tree {
		t.Fatal("Tree = false on the organization dashboard")
	}
	linknet := rowNamed(t, vm, "Linknet")
	if linknet.ParentID != tribeA.ID.String() {
		t.Fatalf("Linknet ParentID = %q, want %q", linknet.ParentID, tribeA.ID)
	}
	if want := it.ID.String() + " " + tribeA.ID.String(); linknet.Ancestors != want {
		t.Fatalf("Linknet Ancestors = %q, want %q", linknet.Ancestors, want)
	}
	if linknet.IndentPx != 2*treeIndentPx {
		t.Fatalf("Linknet IndentPx = %d, want %d", linknet.IndentPx, 2*treeIndentPx)
	}
	if unfiled := rowNamed(t, vm, "Unfiled"); unfiled.ParentID != "" || unfiled.IndentPx != 0 {
		t.Fatalf("Unfiled = parent %q indent %d, want root", unfiled.ParentID, unfiled.IndentPx)
	}
}

func TestProjectTree_FolderRowCountsPassingAtAnyDepthAndNamesFailing(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	tribeB := f.folder("Tribe lead B", &it)
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("Dignas", &tribeA, "pass", intPtr(95))
	f.project("ANRI", &tribeB, "fail", intPtr(55))
	vm := f.build()

	row := rowNamed(t, vm, "IT Services")
	if !row.IsFolder || row.Score != "1 / 3 passing" || row.Tone != "fail" || row.BarTone != "fail" {
		t.Fatalf("IT Services = folder %v, score %q, tone %q, bar %q", row.IsFolder, row.Score, row.Tone, row.BarTone)
	}
	if want := "3 projects · failing: Linknet 42, ANRI 55"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
	if got := rowNamed(t, vm, "Tribe lead A").Score; got != "1 / 2 passing" {
		t.Fatalf("Tribe lead A score = %q, want 1 / 2 passing", got)
	}
}

func TestProjectTree_EightyPercentIsGreenButAnyFailureKeepsTheDotRed(t *testing.T) {
	f := newTreeFixture(t)
	eighty := f.folder("Eighty", nil)
	seventyFive := f.folder("SeventyFive", nil)
	for i := 0; i < 4; i++ {
		f.project("pass80-"+string(rune('a'+i)), &eighty, "pass", intPtr(90))
	}
	f.project("fail80", &eighty, "fail", intPtr(40))
	for i := 0; i < 3; i++ {
		f.project("pass75-"+string(rune('a'+i)), &seventyFive, "pass", intPtr(90))
	}
	f.project("fail75", &seventyFive, "fail", intPtr(40))
	vm := f.build()

	e := rowNamed(t, vm, "Eighty")
	if e.BarTone != "pass" || e.Tone != "fail" || e.ScorePct != "80%" {
		t.Fatalf("80%% folder = bar %q dot %q pct %q, want pass/fail/80%%", e.BarTone, e.Tone, e.ScorePct)
	}
	if s := rowNamed(t, vm, "SeventyFive"); s.BarTone != "fail" {
		t.Fatalf("75%% folder bar = %q, want fail", s.BarTone)
	}
}

func TestProjectTree_EmptyFolderShowsNoVerdict(t *testing.T) {
	f := newTreeFixture(t)
	f.folder("Empty", nil)
	f.project("Somewhere", nil, "pass", intPtr(90))
	row := rowNamed(t, f.build(), "Empty")
	if row.Score != "–" || row.Tone != "none" || row.BarTone != "none" {
		t.Fatalf("empty folder = score %q tone %q bar %q, want –/none/none", row.Score, row.Tone, row.BarTone)
	}
	if row.Subtext != "0 projects · 0 components" {
		t.Fatalf("subtext = %q", row.Subtext)
	}
	if row.DeleteBlocked != "" {
		t.Fatalf("DeleteBlocked = %q on an empty folder, want empty", row.DeleteBlocked)
	}
}

func TestProjectTree_UnmeasuredProjectsAreCalledOutNotCounted(t *testing.T) {
	f := newTreeFixture(t)
	product := f.folder("Product", nil)
	f.project("AlurKerja", &product, "pass", intPtr(84))
	f.project("Never reported", &product, "unmeasured", nil)
	f.project("No components", &product, "", nil)
	vm := f.build()

	row := rowNamed(t, vm, "Product")
	if row.Score != "1 / 1 passing" {
		t.Fatalf("score = %q, want 1 / 1 passing", row.Score)
	}
	if !strings.Contains(row.Subtext, "2 not measured") {
		t.Fatalf("subtext = %q, want it to call out 2 not measured", row.Subtext)
	}
	if nc := rowNamed(t, vm, "No components"); nc.Subtext != "0 components" || nc.Tone != "none" {
		t.Fatalf("componentless project row = %+v, want a 0 components row with no verdict", nc)
	}
	if row.DeleteBlocked == "" {
		t.Fatal("DeleteBlocked is empty on a folder holding projects")
	}
}

func TestProjectTree_FailingNamesCapAtThree(t *testing.T) {
	f := newTreeFixture(t)
	bad := f.folder("Bad", nil)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		f.project(name, &bad, "fail", intPtr(10*(i+1)))
	}
	row := rowNamed(t, f.build(), "Bad")
	if want := "5 projects · failing: A 10, B 20, C 30 +2 more"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
}

func TestProjectTree_KindCellsFollowBindableKindsAndCountShares(t *testing.T) {
	f := newTreeFixture(t)
	fo := f.folder("F", nil)
	f.project("P1", &fo, "pass", intPtr(90))
	f.project("P2", &fo, "fail", intPtr(40))
	row := rowNamed(t, f.build(), "F")

	if len(row.Cells) != len(projectColumns) {
		t.Fatalf("%d cells, want %d (one per projectColumns entry)", len(row.Cells), len(projectColumns))
	}
	for i, col := range projectColumns {
		want := "–"
		if col.Key == "sonarqube" {
			want = "1/2"
		}
		if row.Cells[i].Text != want {
			t.Fatalf("cell %d (%s) = %q, want %q", i, col.Key, row.Cells[i].Text, want)
		}
	}
}

func TestProjectTree_FilingProjectsDoesNotChangeTheOrgScore(t *testing.T) {
	flat := newTreeFixture(t)
	flat.project("A", nil, "pass", intPtr(90))
	flat.project("B", nil, "fail", intPtr(40))

	filed := newTreeFixture(t)
	fo := filed.folder("F", nil)
	filed.project("A", &fo, "pass", intPtr(90))
	filed.project("B", nil, "fail", intPtr(40))

	a, b := flat.build().Score, filed.build().Score
	if a != b {
		t.Fatalf("org score changed by filing a project: %+v vs %+v", a, b)
	}
}

func TestProjectTree_FoldersWithoutComponentsStillShowTheTree(t *testing.T) {
	f := newTreeFixture(t)
	fo := f.folder("Product", nil)
	f.project("Fresh", &fo, "", nil)
	vm := f.build()
	if vm.Empty {
		t.Fatal("Empty = true for an org that has folders")
	}
	if got := rowNames(vm); len(got) != 2 {
		t.Fatalf("rows = %v, want the folder and its project", got)
	}
}

func TestFolderOptions_PathsAndCreateTargetsSkipMaxDepth(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	f.folder("Tribe lead A", &it)
	var parent *repo.Folder
	for i := 1; i <= repo.MaxFolderDepth; i++ {
		fo := f.folder("L"+string(rune('0'+i)), parent)
		parent = &fo
	}
	f.project("P", nil, "pass", intPtr(90))
	vm := f.build()

	labels := map[string]bool{}
	for _, o := range vm.FolderSelect {
		labels[o.Label] = true
	}
	if vm.FolderSelect[0].Value != "" || vm.FolderSelect[0].Label != "Top level" {
		t.Fatalf("first option = %+v, want Top level", vm.FolderSelect[0])
	}
	if !labels["IT Services / Tribe lead A"] {
		t.Fatalf("options %v lack the nested path", labels)
	}
	if labels["L1 / L2 / L3 / L4 / L5 / L6 / L7 / L8"] {
		t.Fatal("a folder at max depth is offered as a parent for a new folder")
	}
	if !labels["L1 / L2 / L3 / L4 / L5 / L6 / L7"] {
		t.Fatal("a folder one level short of max depth is missing")
	}
}

func TestProjectTree_MoveOptionsExcludeTheFolderItsSubtreeAndTooDeepTargets(t *testing.T) {
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	f.folder("Tribe lead A", &it)
	f.folder("Product", nil)
	var parent *repo.Folder
	for i := 1; i <= 7; i++ {
		fo := f.folder("D"+string(rune('0'+i)), parent)
		parent = &fo
	}
	f.project("P", nil, "pass", intPtr(90))
	vm := f.build()

	offered := map[string]bool{}
	for _, o := range rowNamed(t, vm, "IT Services").MoveOptions {
		offered[o.Label] = true
	}
	if offered["IT Services"] || offered["IT Services / Tribe lead A"] {
		t.Fatalf("IT Services may move into itself: %v", offered)
	}
	if !offered["Top level"] || !offered["Product"] {
		t.Fatalf("valid targets missing: %v", offered)
	}
	// IT Services spans 2 levels: under D6 is 6+2 = 8 (fine), under D7 is 9.
	if !offered["D1 / D2 / D3 / D4 / D5 / D6"] {
		t.Fatal("D6 should be offered")
	}
	if offered["D1 / D2 / D3 / D4 / D5 / D6 / D7"] {
		t.Fatal("D7 would nest past the cap and should not be offered")
	}
	if got := rowNamed(t, vm, "Tribe lead A").MoveValue; got != it.ID.String() {
		t.Fatalf("Tribe lead A MoveValue = %q, want its parent %q", got, it.ID)
	}
}
