package harbor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFindArtifact_TagPatternIsAGlob covers the binding field's own name:
// tag_pattern used to be exact-match only, so a binding written as
// "release-*" silently found nothing and read as an image with no scans.
func TestFindArtifact_TagPatternIsAGlob(t *testing.T) {
	// Newest first, the order Harbor returns with sort=-push_time.
	artifacts := []Artifact{
		{Digest: "sha256:newest", Tags: []Tag{{Name: "release-2026-08-29"}}},
		{Digest: "sha256:older", Tags: []Tag{{Name: "release-2026-08-01"}}},
		{Digest: "sha256:oldest", Tags: []Tag{{Name: "dev-2026-07-30"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(artifacts)
	}))
	defer srv.Close()
	c := New(srv.URL, "user", "pass", srv.Client())

	for _, tc := range []struct {
		name, pattern, wantDigest string
	}{
		{"glob picks the newest match", "release-*", "sha256:newest"},
		{"glob can select an older line", "dev-*", "sha256:oldest"},
		{"a plain tag still matches exactly", "release-2026-08-01", "sha256:older"},
		{"single-character wildcard", "dev-2026-07-3?", "sha256:oldest"},
		{"no match is not an error", "staging-*", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art, err := c.FindArtifact(context.Background(), "alurkerja", "alurkerja/auth", tc.pattern)
			if err != nil {
				t.Fatalf("FindArtifact: %v", err)
			}
			got := ""
			if art != nil {
				got = art.Digest
			}
			if got != tc.wantDigest {
				t.Fatalf("digest = %q, want %q", got, tc.wantDigest)
			}
		})
	}
}

// A malformed pattern must say so. Matching nothing would look identical
// to a repository with no scanned images, sending the reader to Harbor
// instead of to the binding they mistyped.
func TestFindArtifact_InvalidPatternIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Artifact{})
	}))
	defer srv.Close()
	c := New(srv.URL, "user", "pass", srv.Client())

	if _, err := c.FindArtifact(context.Background(), "p", "p/r", "[unclosed"); err == nil {
		t.Fatal("FindArtifact accepted a malformed pattern, want an error")
	}
}
