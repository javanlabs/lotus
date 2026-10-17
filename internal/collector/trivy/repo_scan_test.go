package trivy_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/collector"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// fsReport is a trimmed `trivy fs --format json` report: one critical
// (fixable) and one high in a Maven project.
const fsReport = `{"SchemaVersion":2,"ArtifactType":"filesystem","Results":[{"Target":"pom.xml","Class":"lang-pkgs","Type":"pom",
 "Vulnerabilities":[
  {"VulnerabilityID":"CVE-2022-42889","PkgName":"org.apache.commons:commons-text","InstalledVersion":"1.9","FixedVersion":"1.10.0","Severity":"CRITICAL"},
  {"VulnerabilityID":"CVE-2024-0001","PkgName":"com.example:lib","InstalledVersion":"2.0","Severity":"HIGH"}]}]}`

// repoScanFixture is a GitLab stand-in on disk: a bare repository at
// <root>/group/app.git holding one commit with a .trivyignore, and a
// trivy stand-in that writes fsReport to --output and records the
// directory it ran in. It returns the base URL and that record's path.
func repoScanFixture(t *testing.T) (baseURL, ranIn string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(work, ".trivyignore"), []byte("CVE-2000-0000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(root, "group"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(root, "clone", "-q", "--bare", work, filepath.Join(root, "group", "app.git"))

	report := filepath.Join(root, "report.json")
	if err := os.WriteFile(report, []byte(fsReport), 0o644); err != nil {
		t.Fatal(err)
	}
	ranIn = filepath.Join(root, "ran-in")
	script := filepath.Join(root, "trivy")
	body := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--output" ]; then out="$2"; shift; fi
  shift
done
ls -a > "` + ranIn + `"
cp "` + report + `" "$out"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	trivycollector.ConfigureRepoScan(trivycollector.RepoScanConfig{TrivyBin: script, Timeout: time.Minute})
	t.Cleanup(func() { trivycollector.ConfigureRepoScan(trivycollector.RepoScanConfig{}) })
	return "file://" + root, ranIn
}

func openRepoScan(t *testing.T, baseURL string) collector.Session {
	t.Helper()
	s, err := trivycollector.Collector{}.Open(context.Background(), collector.ConnConfig{
		Provider: "gitlab", BaseURL: baseURL, Secret: "glpat-test",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// A Trivy binding on a GitLab connection clones the project and scans it
// from inside the checkout, so the repository's .trivyignore applies,
// and stores what it found as a "repo" scan of that commit.
func TestRepoScan_ClonesAndScansTheRepository(t *testing.T) {
	baseURL, ranIn := repoScanFixture(t)
	res, err := openRepoScan(t, baseURL).Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/app"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if listing, _ := os.ReadFile(ranIn); !strings.Contains(string(listing), ".trivyignore") {
		t.Errorf("trivy did not run inside the checkout; it saw:\n%s", listing)
	}

	var d struct {
		Source string
		Digest string
		Counts trivycollector.Counts
	}
	if err := json.Unmarshal(res.Detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Source != "repo" || len(d.Digest) != 40 {
		t.Errorf("source %q, digest %q; want repo and the commit SHA", d.Source, d.Digest)
	}
	if d.Counts.Critical != 1 || d.Counts.High != 1 || d.Counts.Fixable != 1 {
		t.Errorf("counts = %+v, want 1 critical, 1 high, 1 fixable", d.Counts)
	}
	if res.Headline.Status != collector.StatusFail {
		t.Errorf("status = %s, want fail on a critical", res.Headline.Status)
	}
}

// A project path that is not there is not found — three of those in a
// row mark the binding broken, as for any other collector — and the
// token appears in no error.
func TestRepoScan_MissingProjectIsNotFound(t *testing.T) {
	baseURL, _ := repoScanFixture(t)
	_, err := openRepoScan(t, baseURL).Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/nope"})
	if collector.ClassOf(err) != collector.ErrClassNotFound {
		t.Fatalf("err = %v (%s), want not found", err, collector.ClassOf(err))
	}
	if strings.Contains(err.Error(), "glpat-test") {
		t.Errorf("the token leaked into the error: %v", err)
	}
}

func TestRepoScan_RejectsAPathThatIsNotAProject(t *testing.T) {
	baseURL, _ := repoScanFixture(t)
	for _, key := range []string{"", "--upload-pack=x", "group/../../etc"} {
		_, err := openRepoScan(t, baseURL).Collect(context.Background(), collector.CollectRequest{ExternalKey: key})
		if collector.ClassOf(err) != collector.ErrClassConfig {
			t.Errorf("%q: err = %v, want a config error", key, err)
		}
	}
}

// A repository scan's result is stored like any other Trivy scan, as a
// "repo" snapshot of the commit it scanned.
func TestRepoScan_PersistsAsARepoSnapshot(t *testing.T) {
	baseURL, _ := repoScanFixture(t)
	res, err := openRepoScan(t, baseURL).Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/app"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	db := dbtest.Open(t)
	orgID, componentID, bindingID := seedTrivyFixture(t, db, "acme-repo-scan")
	scope := tenant.FromRun(db, orgID)
	if err := trivycollector.Persist(context.Background(), scope, componentID, bindingID, res); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	snap, err := repo.LatestTrivySnapshot(context.Background(), scope, bindingID)
	if err != nil {
		t.Fatalf("LatestTrivySnapshot: %v", err)
	}
	if snap.Source != "repo" || snap.Digest == nil || len(*snap.Digest) != 40 || snap.Critical != 1 || len(snap.RawJSON) == 0 {
		t.Fatalf("snapshot = source %q, digest %v, critical %d, %d bytes of report", snap.Source, snap.Digest, snap.Critical, len(snap.RawJSON))
	}
}
