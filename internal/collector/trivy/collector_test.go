package trivy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/collector"
	trivycollector "github.com/uyab/lotus/internal/collector/trivy"
)

func fakeHarborServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		if user != "good-user" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v2.0/users/current":
			fmt.Fprint(w, `{"username":"good-user"}`)
		case "/api/v2.0/projects":
			fmt.Fprint(w, `[{"name":"proj"}]`)
		case "/api/v2.0/projects/proj/repositories":
			fmt.Fprint(w, `[{"id":1,"name":"proj/app","artifact_count":2,"pull_count":10}]`)
		case "/api/v2.0/projects/proj/repositories/app/artifacts":
			fmt.Fprint(w, `[
				{"digest":"sha256:new","type":"IMAGE","push_time":"2026-01-02T00:00:00Z",
				 "tags":[{"name":"latest","push_time":"2026-01-02T00:00:00Z"},{"name":"v1.2.0","push_time":"2026-01-02T00:00:00Z"}],
				 "scan_overview":{"application/vnd.security.vulnerability.report; version=1.1":
					{"severity":"Critical","summary":{"total":5,"fixable":3,"summary":{"Critical":1,"High":2,"Medium":2}},"end_time":"2026-01-02T01:00:00Z"}}},
				{"digest":"sha256:old","type":"IMAGE","push_time":"2026-01-01T00:00:00Z","tags":[{"name":"v1.1.0"}]}
			]`)
		case "/api/v2.0/projects/proj/repositories/app/artifacts/sha256:new/additions/vulnerabilities":
			fmt.Fprint(w, `{"application/vnd.security.vulnerability.report; version=1.1":{"severity":"Critical","vulnerabilities":[{"id":"CVE-2026-1","package":"openssl","severity":"Critical"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func openTrivySession(t *testing.T, srv *httptest.Server, secret string) collector.Session {
	t.Helper()
	c := trivycollector.Collector{}
	session, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: secret, Client: srv.Client()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return session
}

func TestCollect_LatestScannedArtifact_ReturnsFactsAndHeadline(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if err := collector.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	if result.Headline.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail (1 critical, default fail_on=critical)", result.Headline.Status)
	}
	if result.Headline.CountValue == nil || *result.Headline.CountValue != 5 {
		t.Fatalf("CountValue = %v, want 5", result.Headline.CountValue)
	}
}

func TestCollect_TagPatternPinsToExactTag(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{
		ExternalKey: "proj/app", Config: []byte(`{"tag_pattern":"v1.1.0"}`),
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// v1.1.0 only tags the older, unscanned artifact — no scan_overview,
	// so severity counts are all zero and the headline passes.
	if result.Headline.Status != collector.StatusPass {
		t.Fatalf("Status = %q, want pass (pinned tag has no scan data)", result.Headline.Status)
	}
}

func TestCollect_OrgSettingsFailOnAnyEscalatesStatus(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{
		ExternalKey: "proj/app", OrgSettings: []byte(`{"trivy_fail_on":"any"}`),
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Headline.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail (fail_on=any, total>0)", result.Headline.Status)
	}
}

func TestCollect_UnauthorizedReturnsErrAuth(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "bad-user:bad-pass")
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj/app"})
	if collector.ClassOf(err) != collector.ErrClassAuth {
		t.Fatalf("ClassOf = %q, want auth", collector.ClassOf(err))
	}
}

func TestDiscover_ListsRepositoriesAcrossProjects(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	candidates, err := session.Discover(context.Background(), collector.DiscoverQuery{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ExternalKey != "proj/app" {
		t.Fatalf("candidates = %+v, want [proj/app]", candidates)
	}
}

func TestValidate_UnknownRepositoryNotFound(t *testing.T) {
	srv := fakeHarborServer(t)
	defer srv.Close()
	session := openTrivySession(t, srv, "good-user:good-pass")
	defer session.Close()

	resolved, err := session.Validate(context.Background(), collector.BindingConfig{ExternalKey: "proj/does-not-exist"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resolved.OK {
		t.Fatal("expected OK=false for a nonexistent repository")
	}
}

func TestOpen_RejectsMissingCredential(t *testing.T) {
	c := trivycollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://harbor.example.com"})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

func TestOpen_RejectsSecretWithoutColon(t *testing.T) {
	c := trivycollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://harbor.example.com", Secret: "no-colon-here"})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}
