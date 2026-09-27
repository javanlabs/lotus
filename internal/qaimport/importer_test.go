package qaimport_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/blob"
	"github.com/uyab/lotus/internal/db"
	"github.com/uyab/lotus/internal/dbtest"
	"github.com/uyab/lotus/internal/qaimport"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
	"github.com/uyab/lotus/internal/repo/legacyqa"
	"github.com/uyab/lotus/internal/repo/legacyqa/legacyqatest"
	"github.com/uyab/lotus/internal/tenant"
)

func lid(n string) uuid.UUID { return uuid.MustParse("00000000-0000-0000-0000-0000000000" + n) }

type env struct {
	db     *sqlx.DB
	s      tenant.Scope
	orgID  uuid.UUID
	legacy *legacyqa.Reader
	store  blob.Store
	opt    qaimport.Options
}

func newEnv(t *testing.T) env {
	t.Helper()
	dsn := dbtest.DSN(t)
	legacyDSN := legacyqatest.LoadFixture(t, dsn)
	app, err := db.OpenApp(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	var orgID uuid.UUID
	if err := app.Get(&orgID, `INSERT INTO organizations (subdomain, name) VALUES ('javan', 'Javan') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	r, err := legacyqa.Open(legacyDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	store, _ := blob.NewFS(t.TempDir())
	files := t.TempDir()
	os.MkdirAll(filepath.Join(files, "public", "2026-09-01"), 0o755)
	os.WriteFile(filepath.Join(files, "public", "2026-09-01", "shot.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644)
	os.WriteFile(filepath.Join(files, "public", "2026-09-01", "log.txt"), []byte("trace"), 0o644)
	return env{
		db: app, s: tenant.FromRun(app, orgID), orgID: orgID, legacy: r, store: store,
		opt: qaimport.Options{
			OrgID:         orgID,
			UsersMap:      map[string]string{"ishaq@gmail.com": "admin@javan.co.id"},
			FolderName:    "QA Tools (imported)",
			DormantBefore: time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC),
			FilesDir:      files,
		},
	}
}

func (e env) run(t *testing.T) *qaimport.Report {
	t.Helper()
	rep, err := qaimport.New(e.db, e.legacy, e.store, e.opt).Run(context.Background())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return rep
}

func (e env) portal(t *testing.T) repo.Project {
	t.Helper()
	ps, _ := repo.ListProjects(context.Background(), e.s, true)
	for _, p := range ps {
		if p.Slug == "s00652-portal" {
			return p
		}
	}
	t.Fatalf("portal project not created; projects = %+v", ps)
	return repo.Project{}
}

func TestImport_UsersProjectsLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rep := e.run(t)

	// Users: the two Ishaq accounts are one admin; the dormant account has a
	// user row but no membership; the client is a guest.
	admin, _ := global.UserByEmail(ctx, e.db, "admin@javan.co.id")
	if admin == nil {
		t.Fatal("admin user missing")
	}
	if m, _ := global.ActiveMembership(ctx, e.db, admin.ID, e.orgID); m == nil || m.RoleKey != "admin" {
		t.Fatalf("admin membership = %+v", m)
	}
	if u, _ := global.UserByEmail(ctx, e.db, "ishaq@gmail.com"); u != nil {
		t.Fatal("alias email got its own user")
	}
	old, _ := global.UserByEmail(ctx, e.db, "old@javan.co.id")
	if old == nil {
		t.Fatal("dormant user row missing (needed for attribution)")
	}
	if m, _ := global.ActiveMembership(ctx, e.db, old.ID, e.orgID); m != nil {
		t.Fatal("dormant user got a membership")
	}
	client, _ := global.UserByEmail(ctx, e.db, "client@bank.example")
	if m, _ := global.ActiveMembership(ctx, e.db, client.ID, e.orgID); m == nil || m.RoleKey != "guest" {
		t.Fatalf("client membership = %+v", m)
	}

	// Projects: only the portal (Empty has no data, the deleted one is skipped), filed in the import folder.
	p := e.portal(t)
	folders, _ := repo.ListFolders(ctx, e.s)
	if len(folders) != 1 || p.FolderID == nil || *p.FolderID != folders[0].ID || folders[0].Name != "QA Tools (imported)" {
		t.Fatalf("folder = %+v, project folder = %v", folders, p.FolderID)
	}
	if ok, _ := repo.IsProjectMember(ctx, e.s, p.ID, client.ID); !ok {
		t.Fatal("guest not added to its project")
	}
	tester, _ := global.UserByEmail(ctx, e.db, "tester@javan.co.id")
	if ok, _ := repo.IsProjectMember(ctx, e.s, p.ID, tester.ID); ok {
		t.Fatal("internal tester added to project_members; only guests belong there")
	}

	// Sections: Auth > Login, "auth (2)", and Old archived.
	secs, _ := repo.ListQASections(ctx, e.s, p.ID)
	names := map[string]*repo.QASection{}
	for i := range secs {
		names[secs[i].Name] = &secs[i]
	}
	if names["Auth"] == nil || names["auth (2)"] == nil || names["Login"] == nil || names["Old"] == nil || len(secs) != 4 {
		t.Fatalf("sections = %+v", secs)
	}
	if names["Old"].ArchivedAt == nil || *names["Login"].ParentID != names["Auth"].ID {
		t.Fatal("section tree or archive flag wrong")
	}

	// Cases.
	c1, err := repo.GetQACaseByKey(ctx, e.s, p.ID, 1)
	if err != nil || c1.Title != "Login valid" || c1.Priority != nil || c1.Precondition != "" ||
		c1.Notes != "https://taiga.javan.id/x" || len(c1.Steps) != 2 || c1.Steps[0].Action != "Open https://portal" {
		t.Fatalf("case 1 = %+v, %v", c1, err)
	}
	if c1.CreatedBy == nil || *c1.CreatedBy != admin.ID {
		t.Fatal("case 1 author not mapped")
	}
	c5, err := repo.GetQACaseByKey(ctx, e.s, p.ID, 5)
	if err != nil || c5.Title != `Login <b>invalid</b> "é" 🚀` || c5.Priority == nil || *c5.Priority != "high" ||
		!c5.Negative || c5.Steps[0].Expected != "" || c5.Steps[0].Action != "Masukkan sandi salah 🚫" {
		t.Fatalf("renumbered case = %+v, %v", c5, err)
	}
	c4, _ := repo.GetQACaseByKey(ctx, e.s, p.ID, 4)
	if c4 == nil || c4.ArchivedAt == nil {
		t.Fatal("deleted-but-referenced case must be imported archived")
	}
	all, _ := repo.ListQACases(ctx, e.s, p.ID, repo.QACaseFilter{IncludeArchived: true})
	if len(all) != 4 {
		t.Fatalf("cases = %d; want 4", len(all))
	}
	if rep.Tables["memberships"].Imported != 3 || rep.ProjectsImported != 1 {
		t.Fatalf("report = %+v", rep)
	}
}
