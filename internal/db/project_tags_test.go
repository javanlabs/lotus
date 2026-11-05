package db_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
)

// Migration 00039 lands the tags in production with the deploy: every
// project filed in the top-level folder ITSD gets PROJECT, every project
// in PRODUCT gets PRODUCT, and nothing else is tagged. The folders
// themselves stay exactly as they were — only the UI stops reading them.
func TestProjectTags_FoldersBecomeTags(t *testing.T) {
	dsn := dbtest.DSN(t)
	if err := db.MigrateDownTo(dsn, 38); err != nil { // back to before 00039
		t.Fatalf("migrate down: %v", err)
	}
	conn, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	mustGet := func(dst *uuid.UUID, q string, args ...any) {
		t.Helper()
		if err := conn.Get(dst, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var orgID, itsd, product, other, deletedITSD uuid.UUID
	mustGet(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('acme', 'Acme') RETURNING id`)
	mustGet(&itsd, `INSERT INTO folders (org_id, name) VALUES ($1, 'ITSD') RETURNING id`, orgID)
	mustGet(&product, `INSERT INTO folders (org_id, name) VALUES ($1, 'PRODUCT') RETURNING id`, orgID)
	mustGet(&other, `INSERT INTO folders (org_id, name) VALUES ($1, 'Other') RETURNING id`, orgID)
	// A deleted ITSD may sit beside the live one (the name index covers live
	// rows only); what is filed in it must not be tagged.
	mustGet(&deletedITSD, `INSERT INTO folders (org_id, name, deleted_at) VALUES ($1, 'ITSD', now()) RETURNING id`, orgID)

	project := func(slug string, folder *uuid.UUID) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		mustGet(&id, `INSERT INTO projects (org_id, slug, name, folder_id) VALUES ($1, $2, $2, $3) RETURNING id`, orgID, slug, folder)
		return id
	}
	pay := project("pay", &itsd)
	hr := project("hr", &itsd)
	alur := project("alurkerja", &product)
	misc := project("misc", &other)
	root := project("root", nil)
	ghost := project("ghost", &deletedITSD)

	var foldersBefore int
	if err := conn.Get(&foldersBefore, `SELECT count(*) FROM folders`); err != nil {
		t.Fatal(err)
	}
	type filed struct {
		ID       uuid.UUID  `db:"id"`
		FolderID *uuid.UUID `db:"folder_id"`
	}
	var filedBefore []filed
	if err := conn.Select(&filedBefore, `SELECT id, folder_id FROM projects ORDER BY id`); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	var tags []string
	if err := conn.Select(&tags, `SELECT name FROM tags WHERE org_id = $1 ORDER BY name`, orgID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tags, []string{"PRODUCT", "PROJECT"}) {
		t.Fatalf("tags = %v, want [PRODUCT PROJECT]", tags)
	}

	tagsOf := func(projectID uuid.UUID) []string {
		t.Helper()
		var names []string
		if err := conn.Select(&names, `SELECT t.name FROM project_tags pt JOIN tags t ON t.id = pt.tag_id
			WHERE pt.project_id = $1 ORDER BY t.name`, projectID); err != nil {
			t.Fatal(err)
		}
		return names
	}
	for name, c := range map[string]struct {
		id   uuid.UUID
		want []string
	}{
		"pay (ITSD)":           {pay, []string{"PROJECT"}},
		"hr (ITSD)":            {hr, []string{"PROJECT"}},
		"alurkerja (PRODUCT)":  {alur, []string{"PRODUCT"}},
		"misc (Other)":         {misc, nil},
		"root (no folder)":     {root, nil},
		"ghost (deleted ITSD)": {ghost, nil},
	} {
		if got := tagsOf(c.id); !slices.Equal(got, c.want) {
			t.Errorf("%s tags = %v, want %v", name, got, c.want)
		}
	}

	var foldersAfter int
	if err := conn.Get(&foldersAfter, `SELECT count(*) FROM folders`); err != nil {
		t.Fatal(err)
	}
	if foldersAfter != foldersBefore {
		t.Errorf("folders = %d after migrating, want %d (untouched)", foldersAfter, foldersBefore)
	}
	var filedAfter []filed
	if err := conn.Select(&filedAfter, `SELECT id, folder_id FROM projects ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(filedAfter) != len(filedBefore) {
		t.Fatalf("projects = %d after migrating, want %d", len(filedAfter), len(filedBefore))
	}
	for i := range filedBefore {
		b, a := filedBefore[i], filedAfter[i]
		if a.ID != b.ID || (a.FolderID == nil) != (b.FolderID == nil) || (a.FolderID != nil && *a.FolderID != *b.FolderID) {
			t.Errorf("project %s folder_id changed: %v -> %v", b.ID, b.FolderID, a.FolderID)
		}
	}
}

func TestProjectTags_ForceRLS(t *testing.T) {
	sqlDB := dbtest.Open(t)
	for _, table := range []string{"tags", "project_tags"} {
		var enabled, forced bool
		if err := sqlDB.QueryRow(`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1`, table).
			Scan(&enabled, &forced); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v; want both", table, enabled, forced)
		}
	}
}

func TestProjectTags_TagNameUniqueIgnoringCase(t *testing.T) {
	sqlDB := dbtest.Open(t)
	var orgA, orgB uuid.UUID
	if err := sqlDB.Get(&orgA, `INSERT INTO organizations (subdomain, name) VALUES ('tag-a', 'A') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Get(&orgB, `INSERT INTO organizations (subdomain, name) VALUES ('tag-b', 'B') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	insert := func(orgID uuid.UUID, name string) error {
		return inOrg(t, sqlDB, orgID, func(tx *sqlx.Tx) error {
			_, err := tx.Exec(`INSERT INTO tags (org_id, name) VALUES ($1, $2)`, orgID, name)
			return err
		})
	}
	if err := insert(orgA, "PROJECT"); err != nil {
		t.Fatal(err)
	}
	err := insert(orgA, "project")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second 'project' in the same org err = %v; want a unique violation (23505)", err)
	}
	if err := insert(orgB, "project"); err != nil {
		t.Fatalf("the same name in another org: %v", err)
	}
}
