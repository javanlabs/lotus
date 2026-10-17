package trivy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/uyab/lotus/internal/clients/gitlab"
	trivyclient "github.com/uyab/lotus/internal/clients/trivy"
	"github.com/uyab/lotus/internal/collector"
)

// A repository scan is the Trivy collector's answer to a component with
// no container image: a Trivy binding on the component's GitLab
// connection (repo.EnsureRepoScanBindings creates it), whose external_key
// is the GitLab project path. Collect clones the default branch, one
// commit deep, and runs `trivy fs` over it from inside the checkout, so
// the repository's own .trivyignore applies exactly as it does when the
// team runs Trivy themselves. The result is the same Result a registry
// scan produces, with Source "repo" and the commit SHA where a registry
// scan keeps the image digest.

// RepoScanConfig is how repository scans run on this host. Zero fields
// take the defaults in ConfigureRepoScan.
type RepoScanConfig struct {
	// TrivyBin and GitBin are the binaries to run, looked up on PATH.
	TrivyBin, GitBin string
	// CacheDir is Trivy's cache (its vulnerability database, ~1.5 GB
	// unpacked); empty uses Trivy's own default under the user's cache.
	CacheDir string
	// Timeout bounds one scan, clone included.
	Timeout time.Duration
}

var repoScan = RepoScanConfig{TrivyBin: "trivy", GitBin: "git", Timeout: 30 * time.Minute}

// scanSlot lets one repository scan run at a time in this process: a
// scan takes a few CPU cores and, on a cold cache, the database download,
// and several at once on the app server would only slow each other and
// the pages being served. A run waiting here is still leased, and shows
// as scanning.
var scanSlot = make(chan struct{}, 1)

// ConfigureRepoScan sets how repository scans run. cmd/lotus calls it once
// at startup, before the scheduler starts.
func ConfigureRepoScan(c RepoScanConfig) {
	if c.TrivyBin == "" {
		c.TrivyBin = "trivy"
	}
	if c.GitBin == "" {
		c.GitBin = "git"
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Minute
	}
	repoScan = c
}

// RepoScanAvailable reports why repository scans cannot run on this host,
// or nil when they can. Without trivy and git there is nothing to run, and
// creating bindings that could only fail would make every repository look
// broken.
func RepoScanAvailable() error {
	for _, bin := range []string{repoScan.TrivyBin, repoScan.GitBin} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found: %w", bin, err)
		}
	}
	return nil
}

// execCommand is exec.CommandContext; tests stand in for trivy with a
// script.
var execCommand = exec.CommandContext

type repoSession struct {
	baseURL, token string
	client         *gitlab.Client
}

func openRepoSession(conn collector.ConnConfig) (collector.Session, error) {
	if conn.BaseURL == "" || conn.Secret == "" {
		return nil, collector.ErrConfig(fmt.Errorf("trivy repository scan: the GitLab connection needs a base URL and a token"))
	}
	return &repoSession{
		baseURL: strings.TrimRight(conn.BaseURL, "/"), token: conn.Secret,
		client: gitlab.New(conn.BaseURL, conn.Secret, conn.Client),
	}, nil
}

func (s *repoSession) Close() error { return nil }

func (s *repoSession) Ping(ctx context.Context) error {
	return classifyGitlab(s.client.Ping(ctx))
}

// Discover lists nothing: a repository scan is created from the
// component's GitLab binding, never picked from a list.
func (s *repoSession) Discover(context.Context, collector.DiscoverQuery) ([]collector.Candidate, error) {
	return nil, nil
}

func (s *repoSession) Validate(ctx context.Context, bc collector.BindingConfig) (collector.Resolved, error) {
	name, err := s.client.ProjectExists(ctx, bc.ExternalKey)
	if err != nil {
		var se *gitlab.StatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
			return collector.Resolved{OK: false, Summary: fmt.Sprintf("no GitLab project found at %q", bc.ExternalKey)}, nil
		}
		return collector.Resolved{}, classifyGitlab(err)
	}
	return collector.Resolved{OK: true, Summary: fmt.Sprintf("will scan the repository %q", name)}, nil
}

func (s *repoSession) Collect(ctx context.Context, req collector.CollectRequest) (collector.Result, error) {
	path := strings.Trim(req.ExternalKey, "/")
	if path == "" || strings.HasPrefix(path, "-") || strings.Contains(path, "..") {
		return collector.Result{}, collector.ErrConfig(fmt.Errorf("trivy repository scan: %q is not a GitLab project path", req.ExternalKey))
	}

	select {
	case scanSlot <- struct{}{}:
		defer func() { <-scanSlot }()
	case <-ctx.Done():
		return collector.Result{}, collector.ErrTimeout(ctx.Err())
	}
	ctx, cancel := context.WithTimeout(ctx, repoScan.Timeout)
	defer cancel()

	work, err := os.MkdirTemp("", "lotus-repo-scan-")
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}
	defer os.RemoveAll(work)
	src := filepath.Join(work, "src")

	// The token travels in an environment-supplied header, never in the
	// URL or the arguments, where ps or an error message could show it.
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+s.token))
	if _, err := s.run(ctx, "", []string{
		"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + auth,
	}, repoScan.GitBin, "clone", "--quiet", "--depth", "1", "--single-branch", "--no-tags", "--",
		s.baseURL+"/"+path+".git", src); err != nil {
		return collector.Result{}, classifyClone(ctx, err)
	}
	commit, err := s.run(ctx, src, nil, repoScan.GitBin, "rev-parse", "HEAD")
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}

	report := filepath.Join(work, "report.json")
	args := []string{"fs", "--quiet", "--format", "json", "--scanners", "vuln",
		"--timeout", repoScan.Timeout.String(), "--output", report}
	if repoScan.CacheDir != "" {
		args = append(args, "--cache-dir", repoScan.CacheDir)
	}
	// Trivy failing — its database download, a timeout, a crash — is this
	// host's problem, not GitLab's: an internal error, which backs off
	// nothing, rather than an upstream one, which would hold back the
	// GitLab connection's own syncs.
	if _, err := s.run(ctx, src, []string{"TRIVY_NO_PROGRESS=true"}, repoScan.TrivyBin, append(args, ".")...); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("trivy repository scan: %w", ctx.Err())
		}
		return collector.Result{}, collector.ErrInternal(err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}
	parsed, err := trivyclient.ParseFSReport(raw)
	if err != nil {
		return collector.Result{}, collector.ErrInternal(err)
	}

	return buildResult(trivyDetail{
		Source:   RepoSource,
		Digest:   strings.TrimSpace(commit),
		Severity: parsed.Severity,
		Counts: Counts{Total: parsed.Total, Fixable: parsed.Fixable, Critical: parsed.Critical, High: parsed.High,
			Medium: parsed.Medium, Low: parsed.Low, Unknown: parsed.Unknown},
		ScanFinishedAt: timePtr(nowFunc()),
		RawReport:      trivyclient.CompactForStorage(raw),
	}, failOnFromSettings(req.OrgSettings))
}

// RepoSource is a repository scan's trivy_snapshots.source.
const RepoSource = "repo"

// run runs bin in dir with extra environment, returning its stdout. A
// failure carries the command's stderr, which is where git and trivy put
// their reason, on one line and cut to a readable length.
func (s *repoSession) run(ctx context.Context, dir string, env []string, bin string, args ...string) (string, error) {
	cmd := execCommand(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), args[0], err, oneLine(stderr.String(), 500))
	}
	return stdout.String(), nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// classifyClone sorts a failed clone by what GitLab said. A bad token is
// an auth failure (it freezes the connection's bindings, as any other
// collector's 401 does); a project the token cannot see is not found.
func classifyClone(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return collector.ErrTimeout(fmt.Errorf("trivy repository scan: clone: %w", ctx.Err()))
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "access denied") || strings.Contains(msg, "authentication failed"):
		return collector.ErrAuth(err)
	case strings.Contains(msg, "not found") || strings.Contains(msg, "could not be found") ||
		strings.Contains(msg, "does not appear to be a git repository") || strings.Contains(msg, "does not exist"):
		return collector.ErrNotFound(err)
	}
	return collector.ErrUpstream(err)
}

func classifyGitlab(err error) error {
	if err == nil {
		return nil
	}
	var se *gitlab.StatusError
	if errors.As(err, &se) {
		switch {
		case se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden:
			return collector.ErrAuth(err)
		case se.StatusCode == http.StatusNotFound:
			return collector.ErrNotFound(err)
		}
	}
	return collector.ErrUpstream(err)
}
