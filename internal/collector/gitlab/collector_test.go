package gitlab_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/uyab/lotus/internal/collector"
	glcollector "github.com/uyab/lotus/internal/collector/gitlab"
)

func fakeGitLabServer(t *testing.T, languagesFail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/projects/group/repo":
			fmt.Fprint(w, `{"id":1,"default_branch":"main","visibility":"private","archived":false,"web_url":"https://gitlab.example.com/group/repo","last_activity_at":"2026-01-01T00:00:00Z"}`)
		case r.URL.Path == "/api/v4/projects/group/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/v4/projects/group/repo/languages":
			if languagesFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"Go":90.0,"Shell":10.0}`)
		case r.URL.Path == "/api/v4/projects/group/repo/repository/branches":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"name":"main","protected":true,"commit":{"committed_date":"2026-01-01T00:00:00Z"}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.URL.Path == "/api/v4/projects/group/repo/pipelines":
			fmt.Fprint(w, `[{"id":1,"status":"success","ref":"main","web_url":"https://gitlab.example.com/pipe/1"}]`)
		case r.URL.Path == "/api/v4/projects/group/repo/pipelines/1":
			fmt.Fprint(w, `{"id":1,"status":"success","duration":42,"finished_at":"2026-01-01T01:00:00Z"}`)
		case r.URL.Path == "/api/v4/projects/group/repo/repository/commits":
			fmt.Fprint(w, `[{"id":"abc","committer_name":"Alice"}]`)
		case r.URL.Path == "/api/v4/user":
			fmt.Fprint(w, `{"username":"tester"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCollect_LanguagesFailureIsPartialWithOneWarningAndNoZeroFact(t *testing.T) {
	srv := fakeGitLabServer(t, true)
	defer srv.Close()

	c := glcollector.Collector{}
	session, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Step != "languages" {
		t.Fatalf("Warnings = %+v, want exactly one for step=languages", result.Warnings)
	}
	if err := collector.ValidateResult(result); err != nil {
		t.Fatalf("a partial result with other facts present must still validate: %v", err)
	}
	// Branches/pipelines/commits facts are still present — only languages failed.
	found := map[string]bool{}
	for _, f := range result.Facts {
		found[f.Key] = true
	}
	for _, want := range []string{"gitlab.branches_total", "gitlab.pipeline_success_rate", "gitlab.commits_30d"} {
		if !found[want] {
			t.Errorf("expected fact %q to be present despite the languages failure", want)
		}
	}
}

func TestCollect_ProjectNotFoundReturnsErrNotFound(t *testing.T) {
	srv := fakeGitLabServer(t, false)
	defer srv.Close()

	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/missing"})
	if err == nil {
		t.Fatal("expected an error for a missing project")
	}
	if collector.ClassOf(err) != collector.ErrClassNotFound {
		t.Fatalf("ClassOf = %q, want not_found", collector.ClassOf(err))
	}
}

func TestCollect_UnauthorizedReturnsErrAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "bad-token", Client: srv.Client()})
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if err == nil {
		t.Fatal("expected an error for a bad token")
	}
	if collector.ClassOf(err) != collector.ErrClassAuth {
		t.Fatalf("ClassOf = %q, want auth", collector.ClassOf(err))
	}
}

func TestCollect_RateLimitedReturnsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "group/repo"})
	if collector.ClassOf(err) != collector.ErrClassRateLimited {
		t.Fatalf("ClassOf = %q, want rate_limited", collector.ClassOf(err))
	}
	if collector.RetryAfterOf(err) != 30*time.Second {
		t.Fatalf("RetryAfterOf = %v, want 30s", collector.RetryAfterOf(err))
	}
}

func TestPing_AuthenticatesAgainstUserEndpoint(t *testing.T) {
	srv := fakeGitLabServer(t, false)
	defer srv.Close()

	c := glcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	defer session.Close()

	if err := session.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestOpen_RejectsMissingCredential(t *testing.T) {
	c := glcollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://gitlab.example.com"})
	if err == nil {
		t.Fatal("expected an error for a missing secret")
	}
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

// TestCollect_HonoursPipelineSampleAndCommitWindow covers two binding
// fields the bind form has always offered and the client always ignored:
// a binding that set them got the built-in 20 pipelines and 30 days
// regardless. A control that does nothing is worse than no control.
func TestCollect_HonoursPipelineSampleAndCommitWindow(t *testing.T) {
	var pipelinesQuery, commitsQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pipelines"):
			pipelinesQuery = r.URL.RawQuery
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/repository/commits"):
			// FetchHealth asks for commits twice: once windowed (velocity)
			// and once unwindowed. Only the windowed one carries `since`.
			if strings.Contains(r.URL.RawQuery, "since=") {
				commitsQuery = r.URL.RawQuery
			}
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/repository/branches"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/languages"):
			fmt.Fprint(w, `{"Go":100}`)
		default:
			fmt.Fprint(w, `{"id":1,"default_branch":"main"}`)
		}
	}))
	defer srv.Close()

	c := glcollector.Collector{}
	session, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "token", Client: srv.Client()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	if _, err := session.Collect(context.Background(), collector.CollectRequest{
		ExternalKey: "group/repo",
		Config:      []byte(`{"default_branch":"main","pipeline_sample_size":5,"commit_window_days":7}`),
	}); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if !strings.Contains(pipelinesQuery, "per_page=5") {
		t.Fatalf("pipelines query = %q, want per_page=5 from the binding", pipelinesQuery)
	}
	q, err := url.ParseQuery(commitsQuery)
	if err != nil {
		t.Fatalf("parse commits query: %v", err)
	}
	at, err := time.Parse(time.RFC3339, q.Get("since"))
	if err != nil {
		t.Fatalf("parse since %q: %v", q.Get("since"), err)
	}
	if days := int(time.Since(at).Hours() / 24); days < 6 || days > 8 {
		t.Fatalf("commit window = %d days, want the binding's 7", days)
	}
}
