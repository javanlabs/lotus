package sentry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/collector"
	sentrycollector "github.com/uyab/lotus/internal/collector/sentry"
)

func fakeSentryServer(t *testing.T, projectHits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/0/organizations/javan/":
			fmt.Fprint(w, `{"slug":"javan"}`)
		case r.URL.Path == "/api/0/organizations/javan/projects/":
			fmt.Fprint(w, `[{"slug":"backend","name":"Backend"},{"slug":"frontend","name":"Frontend"}]`)
		case r.URL.Path == "/api/0/projects/javan/backend/":
			if projectHits != nil {
				*projectHits++
			}
			fmt.Fprint(w, `{"id":"209","slug":"backend","name":"Backend","platform":"go",
				"teams":[{"id":"57","slug":"platform"}],"organization":{"slug":"javan"}}`)
		case r.URL.Path == "/api/0/projects/javan/frontend/":
			fmt.Fprint(w, `{"id":"210","slug":"frontend","name":"Frontend","platform":"javascript",
				"teams":[{"id":"57","slug":"platform"}],"organization":{"slug":"javan"}}`)
		case r.URL.Path == "/api/0/projects/javan/backend/tags/service_name/values/":
			fmt.Fprint(w, `[{"value":"reported-before"},{"value":"bpm-service"}]`)
		case r.URL.Path == "/api/0/projects/javan/does-not-exist/":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"not found"}`)
		case r.URL.Path == "/api/0/projects/javan/backend/issues/" || r.URL.Path == "/api/0/projects/javan/frontend/issues/":
			q := r.URL.Query().Get("query")
			if q == "is:unresolved service_name:\"bpm-service\"" {
				fmt.Fprint(w, `[{"id":"1","substatus":"escalating","count":"500"}]`)
				return
			}
			if q == "is:unresolved" {
				// Dedicated mode: whole-project query, no tag filter.
				fmt.Fprint(w, `[{"id":"1","substatus":"ongoing","count":"5"},{"id":"2","substatus":"new","count":"3"}]`)
				return
			}
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func openSession(t *testing.T, srv *httptest.Server, org string) collector.Session {
	t.Helper()
	c := sentrycollector.Collector{}
	connCfg, _ := json.Marshal(map[string]string{"org": org})
	session, err := c.Open(context.Background(), collector.ConnConfig{
		BaseURL: srv.URL, Secret: "good-token", Client: srv.Client(), Config: connCfg,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return session
}

func TestCollect_SharedModeAppliesTagFilterAndGrades(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{
		"mode": "shared", "service_tag_key": "service_name", "service_tag_value": "bpm-service",
	})
	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if err := collector.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	if len(result.Children) != 1 {
		t.Fatalf("got %d children, want 1 (opt-in environments empty -> single \"all\" child)", len(result.Children))
	}
	child := result.Children[0]
	if len(child.Facts) < 6 {
		t.Fatalf("got %d facts, want at least 6", len(child.Facts))
	}
	// 1 escalating issue, 500 events -> a bad grade.
	if child.Headline.Grade == "" || child.Headline.Grade == "A" {
		t.Fatalf("Grade = %q, want a worse grade for an escalating issue with 500 events", child.Headline.Grade)
	}
}

func TestCollect_DedicatedModeSendsNoTagFilter(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated"})
	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Children) != 1 {
		t.Fatalf("got %d children, want 1", len(result.Children))
	}
	if result.Children[0].Headline.CountValue == nil || *result.Children[0].Headline.CountValue != 2 {
		t.Fatalf("CountValue = %v, want 2 (dedicated mode's unfiltered query matches both issues)", result.Children[0].Headline.CountValue)
	}
}

func TestCollect_OptInEnvironmentsFanOutToChildren(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": []string{"production", "staging"}})
	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Children) != 2 {
		t.Fatalf("got %d children, want 2 (one per named environment)", len(result.Children))
	}
}

func TestCollect_TooManyEnvironmentsIsCappedWithWarning(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	envs := make([]string, 15)
	for i := range envs {
		envs[i] = fmt.Sprintf("env-%d", i)
	}
	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated", "environments": envs})
	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Children) != 10 {
		t.Fatalf("got %d children, want capped at 10", len(result.Children))
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning about the environment cap")
	}
}

func TestCollect_MissingModeReturnsErrConfig(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: []byte(`{}`)})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

func TestCollect_UnknownProjectReturnsErrNotFound(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	cfg, _ := json.Marshal(map[string]any{"mode": "dedicated"})
	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "does-not-exist", Config: cfg})
	if collector.ClassOf(err) != collector.ErrClassNotFound {
		t.Fatalf("ClassOf = %q, want not_found", collector.ClassOf(err))
	}
}

func TestDiscover_ListsProjectsInOrg(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	candidates, err := session.Discover(context.Background(), collector.DiscoverQuery{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(candidates))
	}
}

func TestValidate_UnknownProjectNotFound(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	resolved, err := session.Validate(context.Background(), collector.BindingConfig{ExternalKey: "does-not-exist"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resolved.OK {
		t.Fatal("expected OK=false for a nonexistent project")
	}
}

func TestPing_UnauthorizedReturnsErrAuth(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	c := sentrycollector.Collector{}
	connCfg, _ := json.Marshal(map[string]string{"org": "javan"})
	session, err := c.Open(context.Background(), collector.ConnConfig{
		BaseURL: srv.URL, Secret: "bad-token", Client: srv.Client(), Config: connCfg,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	if collector.ClassOf(session.Ping(context.Background())) != collector.ErrClassAuth {
		t.Fatalf("ClassOf = %q, want auth", collector.ClassOf(session.Ping(context.Background())))
	}
}

func TestOpen_RejectsMissingOrg(t *testing.T) {
	c := sentrycollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://sentry.io", Secret: "tok"})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

func TestOpen_RejectsMissingCredential(t *testing.T) {
	c := sentrycollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://sentry.io"})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}

func TestCollect_ZeroIssuesSeparatesCleanFromNoSignal(t *testing.T) {
	srv := fakeSentryServer(t, nil)
	defer srv.Close()
	session := openSession(t, srv, "javan")
	defer session.Close()

	for _, tc := range []struct {
		name, service string
		prior         bool
		wantStatus    collector.Status
		wantScore     bool
	}{
		{"tag seen", "reported-before", false, collector.StatusPass, true},
		{"history only", "gone-from-tags", true, collector.StatusPass, true},
		{"no evidence", "never-reported", false, collector.StatusUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"mode": "shared", "service_tag_key": "service_name", "service_tag_value": tc.service})
			result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "backend", Config: cfg, PriorSignal: tc.prior})
			if err != nil {
				t.Fatal(err)
			}
			if err := collector.ValidateResult(result); err != nil {
				t.Fatal(err)
			}
			h := result.Headline
			if h.Status != tc.wantStatus || (h.Score != nil) != tc.wantScore {
				t.Fatalf("headline = %+v, want status %s, score present %v", h, tc.wantStatus, tc.wantScore)
			}
			if !tc.wantScore && h.StateLabel != "no signal" {
				t.Errorf("state label = %q, want no signal", h.StateLabel)
			}
			if tc.wantScore && *h.Score != 100 {
				t.Errorf("score = %d, want 100", *h.Score)
			}
			hasScoreFact := false
			for _, f := range result.Children[0].Facts {
				hasScoreFact = hasScoreFact || f.Key == "sentry.score"
			}
			if hasScoreFact != tc.wantScore {
				t.Errorf("score fact present = %v, want %v", hasScoreFact, tc.wantScore)
			}
		})
	}
}
