package db_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
)

// Migration 00035 found production holding what the legacy import made:
// a service's -dev, -release and -new-release Harbor images bound to one
// component. It has to keep one per component and kind — the -release
// image, enabled ones first — retire the rest the way a delete does, and
// only then create the key, or the deploy stops on a unique violation.
func TestOneBindingPerKind_RetiresLegacyDuplicatesBeforeTheKey(t *testing.T) {
	dsn := dbtest.DSN(t)
	if err := db.MigrateDownTo(dsn, 34); err != nil { // back to before 00035
		t.Fatalf("migrate down: %v", err)
	}
	conn, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var orgID, projectID, apps, auth, studio, connID uuid.UUID
	mustGet := func(dst *uuid.UUID, q string, args ...any) {
		t.Helper()
		if err := conn.Get(dst, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustGet(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme', 'Acme') RETURNING id`)
	mustGet(&projectID, `INSERT INTO projects (org_id, slug, name) VALUES ($1, 'alurkerja', 'AlurKerja') RETURNING id`, orgID)
	for dst, slug := range map[*uuid.UUID]string{&apps: "app-react-v2", &auth: "authentication-service", &studio: "studio-react"} {
		mustGet(dst, `INSERT INTO components (org_id, project_id, slug, name, kind) VALUES ($1, $2, $3, $3, 'backend') RETURNING id`, orgID, projectID, slug)
	}
	mustGet(&connID, `INSERT INTO integration_connections (org_id, provider, name, base_url) VALUES ($1, 'harbor_trivy', 'Harbor', 'https://harbor.example') RETURNING id`, orgID)
	bind := func(component uuid.UUID, key string, enabled bool) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key, enabled)
			VALUES ($1, $2, $3, 'trivy', $4, $5)`, orgID, component, connID, key, enabled); err != nil {
			t.Fatalf("bind %s: %v", key, err)
		}
	}
	// Production's shape: all disabled, -dev / -release / -new-release.
	bind(apps, "alurkerja-v2/alurkerja-apps-dev", false)
	bind(apps, "alurkerja-v2/alurkerja-apps-release", false)
	bind(apps, "alurkerja-v2/alurkerja-apps-new-release", false)
	bind(auth, "alurkerja-v2/alurkerja-auth-dev", false)
	bind(auth, "alurkerja-v2/alurkerja-auth-release", false)
	// An enabled binding wins over the release preference.
	bind(studio, "alurkerja-v2/alurkerja-studio-dev", true)
	bind(studio, "alurkerja-v2/alurkerja-studio-release", false)

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up over duplicates: %v", err)
	}

	var live []string
	if err := conn.Select(&live, `SELECT external_key FROM metric_bindings WHERE deleted_at IS NULL ORDER BY external_key`); err != nil {
		t.Fatal(err)
	}
	want := []string{"alurkerja-v2/alurkerja-apps-release", "alurkerja-v2/alurkerja-auth-release", "alurkerja-v2/alurkerja-studio-dev"}
	if len(live) != len(want) {
		t.Fatalf("live bindings = %v, want %v", live, want)
	}
	for i := range want {
		if live[i] != want[i] {
			t.Fatalf("live bindings = %v, want %v", live, want)
		}
	}
	var retired int
	if err := conn.Get(&retired, `SELECT count(*) FROM metric_bindings WHERE deleted_at IS NOT NULL AND NOT enabled`); err != nil {
		t.Fatal(err)
	}
	if retired != 4 {
		t.Fatalf("retired = %d, want 4 (kept rows' history stays, the others are soft-deleted)", retired)
	}
	// The key is in place.
	if _, err := conn.Exec(`INSERT INTO metric_bindings (org_id, component_id, connection_id, kind, external_key)
		VALUES ($1, $2, $3, 'trivy', 'another/image')`, orgID, apps, connID); err == nil {
		t.Fatal("a second live trivy binding was accepted after 00035")
	}
}
