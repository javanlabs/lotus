package qaimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestLoadMaps(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(dir, "users.csv")
	os.WriteFile(users, []byte("legacy_email,canonical_email\nIshaq@Gmail.com , admin@javan.co.id\n"), 0o644)
	m, err := LoadUsersMap(users)
	if err != nil || m["ishaq@gmail.com"] != "admin@javan.co.id" {
		t.Fatalf("users map = %v, %v", m, err)
	}
	projects := filepath.Join(dir, "projects.csv")
	id := uuid.New()
	os.WriteFile(projects, []byte("legacy_project_id,lotus_project_slug\n"+id.String()+",portal\n"), 0o644)
	pm, err := LoadProjectsMap(projects)
	if err != nil || pm[id] != "portal" {
		t.Fatalf("projects map = %v, %v", pm, err)
	}
	bad := filepath.Join(dir, "bad.csv")
	os.WriteFile(bad, []byte("email\nx\n"), 0o644)
	if _, err := LoadUsersMap(bad); err == nil {
		t.Fatal("wrong header accepted")
	}
}
