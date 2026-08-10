package integrations_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/integrations"
)

func TestTest_GitLab_AuthenticatedReportsUserAndCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v4/user":
			fmt.Fprint(w, `{"username":"bayu"}`)
		case "/api/v4/projects":
			w.Header().Set("X-Total", "42")
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	result, err := integrations.Test(context.Background(), srv.Client(), "gitlab", srv.URL, "good-token")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result = %+v, want OK", result)
	}
	if result.Summary == "" {
		t.Fatal("expected a non-empty summary")
	}
}

func TestTest_GitLab_BadTokenReportsAuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	result, err := integrations.Test(context.Background(), srv.Client(), "gitlab", srv.URL, "bad-token")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK || !result.AuthFailed {
		t.Fatalf("result = %+v, want AuthFailed", result)
	}
}

func TestTest_SonarQube_ValidatesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		switch r.URL.Path {
		case "/api/authentication/validate":
			fmt.Fprintf(w, `{"valid":%v}`, user == "good-token")
		case "/api/projects/search":
			fmt.Fprint(w, `{"paging":{"total":7}}`)
		}
	}))
	defer srv.Close()

	result, err := integrations.Test(context.Background(), srv.Client(), "sonarqube", srv.URL, "good-token")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("result = %+v, want OK", result)
	}

	badResult, err := integrations.Test(context.Background(), srv.Client(), "sonarqube", srv.URL, "bad-token")
	if err != nil {
		t.Fatalf("Test (bad token): %v", err)
	}
	if badResult.OK || !badResult.AuthFailed {
		t.Fatalf("bad token result = %+v, want AuthFailed", badResult)
	}
}

func TestTest_UnknownProviderErrors(t *testing.T) {
	if _, err := integrations.Test(context.Background(), http.DefaultClient, "not-a-real-provider", "https://example.com", "x"); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
