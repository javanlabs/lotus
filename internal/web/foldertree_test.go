package web

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
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

// build is the top level of the folder view; buildIn opens one folder.
func (f *treeFixture) build() dashboardViewModel {
	return buildProjectDashboardViewModel("", f.projects, f.folders, f.components, f.metrics, nil)
}

func (f *treeFixture) buildIn(folder repo.Folder) dashboardViewModel {
	id := folder.ID
	return buildProjectDashboardViewModel("", f.projects, f.folders, f.components, f.metrics, &id)
}

func failingNames(vm dashboardViewModel) []string {
	out := make([]string, 0, len(vm.FailingRows))
	for _, r := range vm.FailingRows {
		out = append(out, r.Name)
	}
	return out
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

// The folder view is flat: a level shows its own subfolders first, then the
// projects filed directly in it — never the contents of the folders it
// lists. Clicking a folder opens the next level.
func TestFolderView_ShowsOnlyTheCurrentLevelFoldersFirst(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	product := f.folder("Product", nil)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Unfiled", nil, "pass", intPtr(90))
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("AlurKerja", &product, "pass", intPtr(84))

	for _, c := range []struct {
		name string
		vm   dashboardViewModel
		want []string
	}{
		{"top level", f.build(), []string{"IT Services", "Product", "Unfiled"}},
		{"IT Services", f.buildIn(it), []string{"Tribe lead A"}},
		{"Tribe lead A", f.buildIn(tribeA), []string{"Linknet"}},
	} {
		if got := rowNames(c.vm); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("%s rows = %v, want %v", c.name, got, c.want)
		}
		if !c.vm.Tree {
			t.Fatalf("%s: Tree = false on the organization dashboard", c.name)
		}
	}
	if href := rowNamed(t, f.build(), "IT Services").Href; href != "/projects?folder="+it.ID.String() {
		t.Fatalf("folder row Href = %q, want it to open the folder", href)
	}
}

func TestFolderView_BreadcrumbLeadsBackToTheTop(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Linknet", &tribeA, "pass", intPtr(90))
	vm := f.buildIn(tribeA)

	want := []crumb{
		{Name: "Projects", Href: "/projects"},
		{Name: "IT Services", Href: "/projects?folder=" + it.ID.String(), FolderID: it.ID.String()},
		{Name: "Tribe lead A", Href: "/projects?folder=" + tribeA.ID.String(), FolderID: tribeA.ID.String(), Current: true},
	}
	if len(vm.Crumbs) != len(want) {
		t.Fatalf("crumbs = %+v, want %+v", vm.Crumbs, want)
	}
	for i := range want {
		if vm.Crumbs[i] != want[i] {
			t.Fatalf("crumb %d = %+v, want %+v", i, vm.Crumbs[i], want[i])
		}
	}
	if vm.CurrentFolderID != tribeA.ID.String() {
		t.Fatalf("CurrentFolderID = %q, want %q", vm.CurrentFolderID, tribeA.ID)
	}
	if top := f.build(); len(top.Crumbs) != 1 || !top.Crumbs[0].Current || top.CurrentFolderID != "" {
		t.Fatalf("top-level crumbs = %+v (current %q), want just the current Projects crumb", top.Crumbs, top.CurrentFolderID)
	}
}

// "Failing" lists every failing project beneath the current folder at any
// depth, flat, worst first, each saying where it lives — so a team sees
// what to fix without clicking through folders.
func TestFolderView_FailingListsEveryFailingProjectBeneathWithItsPath(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	tribeB := f.folder("Tribe lead B", &it)
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("Dignas", &tribeA, "pass", intPtr(95))
	f.project("ANRI", &tribeB, "fail", intPtr(55))
	f.project("Loose", nil, "fail", intPtr(60))

	top := f.build()
	if got := failingNames(top); strings.Join(got, "|") != "Linknet|ANRI|Loose" {
		t.Fatalf("top-level failing = %v, want Linknet|ANRI|Loose (worst first)", got)
	}
	if top.FailingCount != 3 {
		t.Fatalf("FailingCount = %d, want 3", top.FailingCount)
	}
	if sub := top.FailingRows[0].Subtext; !strings.HasPrefix(sub, "IT Services / Tribe lead A") {
		t.Fatalf("Linknet failing subtext = %q, want it to start with its folder path", sub)
	}
	if sub := top.FailingRows[2].Subtext; !strings.HasPrefix(sub, "Top level") {
		t.Fatalf("Loose failing subtext = %q, want it to say Top level", sub)
	}
	if got := failingNames(f.buildIn(tribeB)); strings.Join(got, "|") != "ANRI" {
		t.Fatalf("Tribe lead B failing = %v, want only ANRI", got)
	}
}

func TestProjectTree_FolderRowCountsPassingAtAnyDepthAndNamesFailing(t *testing.T) {
	t.Parallel()
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
	if want := "3 projects · 2 failing"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
	if got := rowNamed(t, f.buildIn(it), "Tribe lead A").Score; got != "1 / 2 passing" {
		t.Fatalf("Tribe lead A score = %q, want 1 / 2 passing", got)
	}
}

func TestProjectTree_EightyPercentIsGreenButAnyFailureKeepsTheDotRed(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if nc := rowNamed(t, f.buildIn(product), "No components"); nc.Subtext != "0 components" || nc.Tone != "none" {
		t.Fatalf("componentless project row = %+v, want a 0 components row with no verdict", nc)
	}
	if row.DeleteBlocked == "" {
		t.Fatal("DeleteBlocked is empty on a folder holding projects")
	}
}

// A folder row counts its failing projects rather than naming them; the
// "Failing" filter is where they are listed.
func TestProjectTree_FolderSubtextCountsFailingWithoutNamingThem(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	bad := f.folder("Bad", nil)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		f.project(name, &bad, "fail", intPtr(10*(i+1)))
	}
	row := rowNamed(t, f.build(), "Bad")
	if want := "5 projects · 5 failing"; row.Subtext != want {
		t.Fatalf("subtext = %q, want %q", row.Subtext, want)
	}
}

func TestProjectTree_KindCellsFollowBindableKindsAndCountShares(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	f := newTreeFixture(t)
	fo := f.folder("Product", nil)
	f.project("Fresh", &fo, "", nil)
	vm := f.build()
	if vm.Empty {
		t.Fatal("Empty = true for an org that has folders")
	}
	if got := rowNames(vm); len(got) != 1 || got[0] != "Product" {
		t.Fatalf("rows = %v, want the folder", got)
	}
}

// Review focus (final review, item 2): RowCount used to be len(vm.Rows),
// which on the tree includes folder rows — 5 projects in 2 folders reads
// "All 7". "All", the footer and the subtitle must all count PROJECTS.
func TestProjectTree_RowCountCountsProjectsNotFolderRows(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Unfiled", nil, "pass", intPtr(90))
	f.project("Linknet", &tribeA, "fail", intPtr(42))
	f.project("AlurKerja", &it, "pass", intPtr(84))
	vm := f.build()

	if len(vm.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (IT Services + Unfiled)", len(vm.Rows))
	}
	if vm.RowCount != 3 {
		t.Fatalf("RowCount = %d, want 3 (projects at any depth, folders excluded)", vm.RowCount)
	}
	if in := f.buildIn(it); in.RowCount != 2 {
		t.Fatalf("RowCount inside IT Services = %d, want 2 (the projects beneath it)", in.RowCount)
	}
}

// Review focus (final review, item 4): projects exist, none has a
// component and no folder exists either — the page must still show the
// tree (deviation 4: every active project gets a row), not the
// "No repositories yet" empty state, or there is no way to create the
// first folder.
func TestProjectTree_ComponentlessProjectWithNoFoldersShowsTheTreeNotEmptyState(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	f.project("Fresh", nil, "", nil)
	vm := f.build()
	if vm.Empty {
		t.Fatal("Empty = true for an org with a project and no components or folders")
	}
	if got := rowNames(vm); len(got) != 1 || got[0] != "Fresh" {
		t.Fatalf("rows = %v, want just the project", got)
	}
}

// Review focus (final review, item 5): folderDeleteBlocked's hint must
// agree with the server, which refuses to delete a folder only over its
// DIRECT subfolders and projects. r.Projects (the rollup) counts active
// projects at ANY depth, so a folder holding only a subfolder — which
// itself holds projects — used to claim it held those projects too.
func TestProjectTree_DeleteHintCountsOnlyDirectlyFiledProjects(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	outer := f.folder("Outer", nil)
	inner := f.folder("Inner", &outer)
	f.project("A", &inner, "pass", intPtr(90))
	f.project("B", &inner, "fail", intPtr(40))
	vm := f.build()

	row := rowNamed(t, vm, "Outer")
	if want := "Still holds 1 folder · move them out first"; row.DeleteBlocked != want {
		t.Fatalf("DeleteBlocked = %q, want %q (no project count — those are in Inner, not Outer)", row.DeleteBlocked, want)
	}
}

func TestFolderOptions_PathsAndCreateTargetsSkipMaxDepth(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if got := rowNamed(t, f.buildIn(it), "Tribe lead A").MoveValue; got != it.ID.String() {
		t.Fatalf("Tribe lead A MoveValue = %q, want its parent %q", got, it.ID)
	}
}

func TestFolderTrail_JoinsThePageBreadcrumbOnlyInsideAFolder(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	f.project("Linknet", &tribeA, "pass", intPtr(90))

	if trail := folderTrail(f.build().Crumbs, true); trail != nil {
		t.Fatalf("top-level trail = %+v, want none (the organization is already the last crumb)", trail)
	}
	// No "Projects" crumb: the organization's name before the trail is the
	// link to the top level.
	assertTrail(t, "Tribe lead A", folderTrail(f.buildIn(tribeA).Crumbs, true), []httpx.NavCrumb{
		{Label: "IT Services", Href: "/projects?folder=" + it.ID.String(), Drop: true, DropFolder: it.ID.String()},
		{Label: "Tribe lead A"},
	})
	if viewer := folderTrail(f.buildIn(tribeA).Crumbs, false); viewer[0].Drop {
		t.Fatal("a viewer's breadcrumb accepts drops")
	}
}

// A project's pages carry the same breadcrumb as the folder view: every
// folder down to the project, all links back up (the organization's name,
// rendered before the trail, links to the top level).
func TestProjectTrail_IncludesTheFolderPath(t *testing.T) {
	t.Parallel()
	f := newTreeFixture(t)
	it := f.folder("IT Services", nil)
	tribeA := f.folder("Tribe lead A", &it)
	fid := tribeA.ID
	filed := &repo.Project{ID: uuid.New(), Slug: "linknet", Name: "Linknet", FolderID: &fid}
	loose := &repo.Project{ID: uuid.New(), Name: "SyarQ"}
	comp := &repo.Component{ID: uuid.New(), Name: "linknet-api"}

	want := []httpx.NavCrumb{
		{Label: "IT Services", Href: "/projects?folder=" + it.ID.String()},
		{Label: "Tribe lead A", Href: "/projects?folder=" + tribeA.ID.String()},
		{Label: "Linknet"},
	}
	assertTrail(t, "filed project", projectTrail(f.folders, filed, trailMenus{}), want)
	assertTrail(t, "top-level project", projectTrail(f.folders, loose, trailMenus{}), []httpx.NavCrumb{{Label: "SyarQ"}})

	want[2] = httpx.NavCrumb{Label: "Linknet", Href: "/projects/linknet"}
	assertTrail(t, "component", componentTrail(f.folders, filed, comp, trailMenus{}),
		append(want, httpx.NavCrumb{Label: "linknet-api"}))
}

func assertTrail(t *testing.T, name string, got, want []httpx.NavCrumb) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s trail = %+v, want %+v", name, got, want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("%s crumb %d = %+v, want %+v", name, i, got[i], want[i])
		}
	}
}
