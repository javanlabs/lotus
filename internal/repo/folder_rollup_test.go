package repo_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/collector"
	"github.com/uyab/lotus/internal/repo"
)

func testFolder(name string, parent *repo.Folder) repo.Folder {
	f := repo.Folder{ID: uuid.New(), Name: name}
	if parent != nil {
		pid := parent.ID
		f.ParentID = &pid
	}
	return f
}

// treeProject is a project whose Lotus status and score are already
// rolled up, with its SonarQube kind in the same state.
func treeProject(name string, in *repo.Folder, status collector.Status, s *int) repo.TreeProject {
	p := repo.TreeProject{
		ID: uuid.New(), Name: name, Components: 1,
		Rollup: repo.ProjectRollup{
			Status: status, Score: s,
			Kinds: map[string]repo.KindRollup{"sonarqube": {Status: status, Score: s, Count: 1}},
		},
	}
	if in != nil {
		fid := in.ID
		p.FolderID = &fid
	}
	return p
}

func TestRollupFolders_CountsProjectsAtAnyDepth(t *testing.T) {
	t.Parallel()
	it := testFolder("IT Services", nil)
	tribeA := testFolder("Tribe lead A", &it)
	tribeB := testFolder("Tribe lead B", &it)
	got := repo.RollupFolders(
		[]repo.Folder{it, tribeA, tribeB},
		[]repo.TreeProject{
			treeProject("Linknet", &tribeA, collector.StatusFail, score(42)),
			treeProject("Dignas", &tribeA, collector.StatusPass, score(95)),
			treeProject("ANRI", &tribeB, collector.StatusFail, score(55)),
			treeProject("Unfiled", nil, collector.StatusPass, score(90)),
		})

	if s := got[it.ID].Share; s != (repo.PassShare{Passing: 1, Failing: 2}) {
		t.Fatalf("IT Services share = %+v, want 1 passing 2 failing", s)
	}
	if got[it.ID].Projects != 3 || got[it.ID].Components != 3 {
		t.Fatalf("IT Services counts = %d projects %d components, want 3 and 3", got[it.ID].Projects, got[it.ID].Components)
	}
	if s := got[tribeA.ID].Share; s != (repo.PassShare{Passing: 1, Failing: 1}) {
		t.Fatalf("Tribe lead A share = %+v, want 1 passing 1 failing", s)
	}
	if pct, ok := got[it.ID].Share.Percent(); !ok || pct != 33 {
		t.Fatalf("IT Services percent = %d (%v), want 33", pct, ok)
	}
}

func TestRollupFolders_UnknownIsCountedButNotMeasured(t *testing.T) {
	t.Parallel()
	f := testFolder("Product", nil)
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("AlurKerja", &f, collector.StatusPass, score(84)),
		treeProject("New thing", &f, collector.StatusUnknown, nil),
	})
	s := got[f.ID].Share
	if s.Passing != 1 || s.Unknown != 1 || s.Measured() != 1 {
		t.Fatalf("share = %+v (measured %d), want 1 passing, 1 unknown, 1 measured", s, s.Measured())
	}
	if pct, ok := s.Percent(); !ok || pct != 100 {
		t.Fatalf("percent = %d (%v), want 100", pct, ok)
	}
}

func TestRollupFolders_EmptyFolderIsPresentWithNothingMeasured(t *testing.T) {
	t.Parallel()
	f := testFolder("Empty", nil)
	got := repo.RollupFolders([]repo.Folder{f}, nil)
	r, ok := got[f.ID]
	if !ok {
		t.Fatal("empty folder missing from the rollup")
	}
	if _, measured := r.Share.Percent(); measured || r.Projects != 0 {
		t.Fatalf("empty folder = %+v, want nothing measured", r)
	}
}

func TestRollupFolders_FailingWorstFirstUnscoredLast(t *testing.T) {
	t.Parallel()
	f := testFolder("F", nil)
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("Mid", &f, collector.StatusFail, score(55)),
		treeProject("NoScore", &f, collector.StatusFail, nil),
		treeProject("Worst", &f, collector.StatusFail, score(42)),
		treeProject("Fine", &f, collector.StatusPass, score(99)),
	})
	var names []string
	for _, p := range got[f.ID].Failing {
		names = append(names, p.Name)
	}
	want := []string{"Worst", "Mid", "NoScore"}
	if len(names) != len(want) {
		t.Fatalf("failing = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("failing = %v, want %v", names, want)
		}
	}
}

func TestRollupFolders_KindSharesSkipProjectsWithoutThatKind(t *testing.T) {
	t.Parallel()
	f := testFolder("F", nil)
	noSonar := treeProject("NoSonar", &f, collector.StatusPass, score(90))
	noSonar.Rollup.Kinds = map[string]repo.KindRollup{"gitlab": {Status: collector.StatusPass, Score: score(90), Count: 1}}
	got := repo.RollupFolders([]repo.Folder{f}, []repo.TreeProject{
		treeProject("A", &f, collector.StatusPass, score(90)),
		treeProject("B", &f, collector.StatusFail, score(40)),
		noSonar,
	})
	if s := got[f.ID].Kinds["sonarqube"]; s != (repo.PassShare{Passing: 1, Failing: 1}) {
		t.Fatalf("sonarqube share = %+v, want 1 passing 1 failing", s)
	}
	if s := got[f.ID].Kinds["gitlab"]; s != (repo.PassShare{Passing: 1}) {
		t.Fatalf("gitlab share = %+v, want 1 passing", s)
	}
}

func TestPassShare_PercentRoundsHalfUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		share repo.PassShare
		want  int
	}{
		{repo.PassShare{Passing: 4, Failing: 1}, 80},
		{repo.PassShare{Passing: 3, Failing: 1}, 75},
		{repo.PassShare{Passing: 1, Failing: 2}, 33},
		{repo.PassShare{Passing: 2, Failing: 1}, 67},
	}
	for _, c := range cases {
		if got, ok := c.share.Percent(); !ok || got != c.want {
			t.Errorf("%+v percent = %d (%v), want %d", c.share, got, ok, c.want)
		}
	}
}
