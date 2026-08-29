package integrations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/integrations"
)

func TestDiscover_GitLab_Paginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			// A full page (100) so Discover fetches page 2.
			w.Write([]byte("["))
			for i := range 100 {
				if i > 0 {
					w.Write([]byte(","))
				}
				fmt.Fprintf(w, `{"id":%d,"name":"p%d","path_with_namespace":"group/p%d"}`, i, i, i)
			}
			w.Write([]byte("]"))
		case "2":
			fmt.Fprint(w, `[{"id":101,"name":"last","path_with_namespace":"group/last"}]`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	items, err := integrations.Discover(context.Background(), srv.Client(), "gitlab", srv.URL, "token", nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 101 {
		t.Fatalf("got %d items, want 101 (100 from page 1 + 1 from page 2)", len(items))
	}
	if items[100].ExternalKey != "group/last" {
		t.Fatalf("last item external_key = %q, want group/last", items[100].ExternalKey)
	}
}

func TestDiscover_SonarQube_NoAdminScopeIsDegradedNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := integrations.Discover(context.Background(), srv.Client(), "sonarqube", srv.URL, "limited-token", nil)
	if err != integrations.DiscoverDegradedNoAdminScope {
		t.Fatalf("err = %v, want DiscoverDegradedNoAdminScope", err)
	}
}

func TestDiscover_SonarQube_ListsProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"components":[{"key":"proj-a","name":"Project A"},{"key":"proj-b","name":"Project B"}]}`)
	}))
	defer srv.Close()

	items, err := integrations.Discover(context.Background(), srv.Client(), "sonarqube", srv.URL, "token", nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
}

func TestDiscover_Harbor_ListsProjectsThenRepositories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2.0/projects":
			fmt.Fprint(w, `[{"name":"alurkerja-v2"}]`)
		case "/api/v2.0/projects/alurkerja-v2/repositories":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"name":"alurkerja-v2/bpm-release"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	items, err := integrations.Discover(context.Background(), srv.Client(), "harbor_trivy", srv.URL, "user:pass", nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 1 || items[0].ExternalKey != "alurkerja-v2/bpm-release" {
		t.Fatalf("items = %+v, want one item alurkerja-v2/bpm-release", items)
	}
}

func TestDiscover_Sentry_ListsProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"slug":"bpm-service","name":"BPM Service"}]`)
	}))
	defer srv.Close()

	items, err := integrations.Discover(context.Background(), srv.Client(), "sentry", srv.URL, "token", nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 1 || items[0].ExternalKey != "bpm-service" {
		t.Fatalf("items = %+v, want one item bpm-service", items)
	}
}

// A Sentry token can see more than one Sentry organization. Unscoped
// discovery offered all of them, so binding a project could quietly point
// a component at another organization's data.
func TestDiscover_Sentry_ScopesToTheConnectionsOrganization(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[{"slug":"bpm-service","name":"BPM Service"}]`)
	}))
	defer srv.Close()

	if _, err := integrations.Discover(context.Background(), srv.Client(), "sentry", srv.URL, "token",
		json.RawMessage(`{"org":"javan"}`)); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if want := "/api/0/organizations/javan/projects/"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}

	// A connection created before the field existed keeps working, on the
	// endpoint it always used.
	if _, err := integrations.Discover(context.Background(), srv.Client(), "sentry", srv.URL, "token", nil); err != nil {
		t.Fatalf("Discover without org: %v", err)
	}
	if want := "/api/0/projects/"; gotPath != want {
		t.Fatalf("path without org = %q, want %q", gotPath, want)
	}
}

func TestBindingKindForProvider(t *testing.T) {
	cases := map[string]string{
		"gitlab": "gitlab", "sonarqube": "sonarqube", "sentry": "sentry", "harbor_trivy": "trivy",
	}
	for provider, want := range cases {
		got, err := integrations.BindingKindForProvider(provider)
		if err != nil {
			t.Fatalf("BindingKindForProvider(%q): %v", provider, err)
		}
		if got != want {
			t.Errorf("BindingKindForProvider(%q) = %q, want %q", provider, got, want)
		}
	}
	if _, err := integrations.BindingKindForProvider("not-a-provider"); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}
