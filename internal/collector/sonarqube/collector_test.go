package sonarqube_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/clients/sonar"
	"github.com/uyab/lotus/internal/collector"
	sqcollector "github.com/uyab/lotus/internal/collector/sonarqube"
)

func fakeSonarServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		if user != "good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/measures/component":
			fmt.Fprint(w, `{"component":{"measures":[
				{"metric":"coverage","value":"72.5"},
				{"metric":"bugs","value":"3"},
				{"metric":"vulnerabilities","value":"1"},
				{"metric":"code_smells","value":"20"},
				{"metric":"security_hotspots","value":"2"},
				{"metric":"reliability_rating","value":"2"},
				{"metric":"security_rating","value":"1"},
				{"metric":"sqale_rating","value":"3"}
			]}}`)
		case "/api/qualitygates/project_status":
			fmt.Fprint(w, `{"projectStatus":{"status":"OK","conditions":[{"status":"OK","metricKey":"new_coverage","comparator":"LT","errorThreshold":"80","actualValue":"91.0"}]}}`)
		case "/api/project_analyses/search":
			fmt.Fprint(w, `{"analyses":[{"date":"2026-01-01T00:00:00+0000"}]}`)
		case "/api/authentication/validate":
			fmt.Fprint(w, `{"valid":true}`)
		case "/api/components/show":
			if r.URL.Query().Get("component") == "does-not-exist" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"component":{"name":"My Project"}}`)
		case "/api/projects/search":
			fmt.Fprint(w, `{"components":[{"key":"proj-a","name":"Project A"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCollect_ReturnsFactsAndWorstRatingAsGrade(t *testing.T) {
	srv := fakeSonarServer(t)
	defer srv.Close()

	c := sqcollector.Collector{}
	session, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "good-token", Client: srv.Client()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	result, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj-a"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if err := collector.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult: %v", err)
	}
	if len(result.Facts) < 8 {
		t.Fatalf("got %d facts, want at least 8", len(result.Facts))
	}
	// Worst of reliability=B(2), security=A(1), maintainability=C(3) is C.
	if result.Headline.Grade != "C" {
		t.Fatalf("Grade = %q, want C (worst of B/A/C)", result.Headline.Grade)
	}
	// The status is read from the score, not the gate: (72.5 coverage +
	// 60 for the C rating) / 2 × 1 for a passed gate is 66, below 80.
	if result.Headline.Score == nil || *result.Headline.Score != 66 {
		t.Fatalf("Score = %v, want 66", result.Headline.Score)
	}
	if result.Headline.Status != collector.StatusFail {
		t.Fatalf("Status = %q, want fail (66 is below 80 even with the gate passed)", result.Headline.Status)
	}
	// The gate's conditions travel in the Detail, which Persist stores as
	// the snapshot's raw_json — no column of their own.
	var detail sonar.Measures
	if err := json.Unmarshal(result.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.GateConditions) != 1 || detail.GateConditions[0].MetricKey != "new_coverage" || detail.GateConditions[0].ErrorThreshold != "80" {
		t.Fatalf("gate conditions in detail = %+v", detail.GateConditions)
	}
}

func TestCollect_UnauthorizedReturnsErrAuth(t *testing.T) {
	srv := fakeSonarServer(t)
	defer srv.Close()

	c := sqcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "bad-token", Client: srv.Client()})
	defer session.Close()

	_, err := session.Collect(context.Background(), collector.CollectRequest{ExternalKey: "proj-a"})
	if collector.ClassOf(err) != collector.ErrClassAuth {
		t.Fatalf("ClassOf = %q, want auth", collector.ClassOf(err))
	}
}

func TestDiscover_NoAdminScopeReturnsEmptyNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/projects/search" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := sqcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "limited-token", Client: srv.Client()})
	defer session.Close()

	candidates, err := session.Discover(context.Background(), collector.DiscoverQuery{})
	if err != nil {
		t.Fatalf("Discover: %v, want no error for a 403 (degrade to free-text, task 2.4)", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0", len(candidates))
	}
}

func TestValidate_ProjectNotFound(t *testing.T) {
	srv := fakeSonarServer(t)
	defer srv.Close()

	c := sqcollector.Collector{}
	session, _ := c.Open(context.Background(), collector.ConnConfig{BaseURL: srv.URL, Secret: "good-token", Client: srv.Client()})
	defer session.Close()

	resolved, err := session.Validate(context.Background(), collector.BindingConfig{ExternalKey: "does-not-exist"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resolved.OK {
		t.Fatal("expected OK=false for a nonexistent project")
	}
}

func TestOpen_RejectsMissingCredential(t *testing.T) {
	c := sqcollector.Collector{}
	_, err := c.Open(context.Background(), collector.ConnConfig{BaseURL: "https://sonar.example.com"})
	if collector.ClassOf(err) != collector.ErrClassConfig {
		t.Fatalf("ClassOf = %q, want config", collector.ClassOf(err))
	}
}
