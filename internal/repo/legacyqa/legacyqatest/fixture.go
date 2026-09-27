// Package legacyqatest loads a small legacy QA Tools database for tests.
// It lives apart from legacyqa so the lotus binary never links the testing
// package or embeds the fixture.
package legacyqatest

import (
	_ "embed"
	"net/url"
	"testing"

	"github.com/uyab/lotus/internal/db"
)

//go:embed testdata/legacy_schema.sql
var legacySchema string

//go:embed testdata/legacy_fixture.sql
var legacyFixture string

// LoadFixture creates the legacy schema in the database superuserDSN points
// at (a dbtest per-test database), loads the fixture, and returns a DSN
// whose search_path is that schema. Tests only.
func LoadFixture(t testing.TB, superuserDSN string) string {
	t.Helper()
	conn, err := db.Open(superuserDSN)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	for _, script := range []string{legacySchema, legacyFixture} {
		if _, err := conn.Exec(script); err != nil {
			t.Fatalf("load legacy fixture: %v", err)
		}
	}
	u, err := url.Parse(superuserDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", "legacy")
	u.RawQuery = q.Encode()
	return u.String()
}
