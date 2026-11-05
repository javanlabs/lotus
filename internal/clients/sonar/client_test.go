package sonar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The quality gate's conditions come back with the gate's status, so a
// failed gate can say which condition failed and why. The server is a
// stub; no SonarQube is contacted.
func TestFetch_RecordsQualityGateConditions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/measures/component":
			fmt.Fprint(w, `{"component":{"measures":[{"metric":"coverage","value":"90.0"}]}}`)
		case "/api/qualitygates/project_status":
			if r.URL.Query().Get("projectKey") != "proj" {
				t.Errorf("projectKey = %q", r.URL.Query().Get("projectKey"))
			}
			fmt.Fprint(w, `{"projectStatus":{"status":"ERROR","conditions":[
				{"status":"ERROR","metricKey":"new_coverage","comparator":"LT","errorThreshold":"85","actualValue":"82.5"},
				{"status":"OK","metricKey":"new_duplicated_lines_density","comparator":"GT","errorThreshold":"3","actualValue":"1.2"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m, err := New(srv.URL, "tok", srv.Client()).Fetch(context.Background(), "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.QualityGate != "ERROR" || len(m.GateConditions) != 2 {
		t.Fatalf("gate = %q, conditions = %+v", m.QualityGate, m.GateConditions)
	}
	c := m.GateConditions[0]
	if c != (GateCondition{Status: "ERROR", MetricKey: "new_coverage", Comparator: "LT", ErrorThreshold: "85", ActualValue: "82.5"}) || !c.Failed() {
		t.Errorf("first condition = %+v", c)
	}
	if m.GateConditions[1].Failed() {
		t.Error("an OK condition is not what failed the gate")
	}
}

// A gate call that fails leaves no conditions and no status; the measures
// still come back.
func TestFetch_GateFailureLeavesNoConditions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/measures/component" {
			fmt.Fprint(w, `{"component":{"measures":[{"metric":"coverage","value":"90.0"}]}}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	m, err := New(srv.URL, "tok", srv.Client()).Fetch(context.Background(), "proj", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.QualityGate != "" || m.GateConditions != nil || m.Coverage == nil {
		t.Fatalf("measures = %+v", m)
	}
}
