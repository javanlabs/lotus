package integrations_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/integrations"
)

func TestTestBinding_GitLab_FoundAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http decodes the path before the handler sees it, so the
		// %2F this request's PathEscape produced arrives back as a plain
		// "/" here.
		switch r.URL.Path {
		case "/api/v4/projects/group/project":
			fmt.Fprint(w, `{"name_with_namespace":"Group / Project"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	found, err := integrations.TestBinding(context.Background(), srv.Client(), "gitlab", srv.URL, "token", "group/project")
	if err != nil {
		t.Fatalf("TestBinding: %v", err)
	}
	if !found.OK {
		t.Fatalf("found = %+v, want OK", found)
	}

	notFound, err := integrations.TestBinding(context.Background(), srv.Client(), "gitlab", srv.URL, "token", "group/nonexistent")
	if err != nil {
		t.Fatalf("TestBinding: %v", err)
	}
	if notFound.OK {
		t.Fatalf("notFound = %+v, want not OK", notFound)
	}
}

func TestTestBinding_Harbor_RejectsMalformedExternalKey(t *testing.T) {
	result, err := integrations.TestBinding(context.Background(), http.DefaultClient, "trivy", "https://harbor.example.com", "user:pass", "no-slash-here")
	if err != nil {
		t.Fatalf("TestBinding: %v", err)
	}
	if result.OK {
		t.Fatal("expected a malformed (no project/repo split) external_key to be rejected")
	}
}
