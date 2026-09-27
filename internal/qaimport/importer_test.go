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

func TestImport_Execution(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.run(t)
	p := e.portal(t)

	plans, _ := repo.ListQAPlans(ctx, e.s, p.ID)
	byName := map[string]repo.QAPlan{}
	for _, pl := range plans {
		byName[pl.Name] = pl
	}
	if len(plans) != 3 || byName["UAT Plan SIT"].Phase != "uat" || byName["Regresi - September"].Phase != "regression" ||
		byName["Unplanned (imported)"].Phase != "other" {
		t.Fatalf("plans = %+v", plans)
	}
	uat := byName["UAT Plan SIT"]
	if uat.StartsOn == nil || uat.StartsOn.Format("2006-01-02") != "2026-08-31" || uat.ClosedAt != nil {
		t.Fatalf("uat plan = %+v", uat)
	}
	runs, _ := repo.ListQARuns(ctx, e.s, uat.ID)
	if len(runs) != 1 || runs[0].Name != "[UAT] Portal" || runs[0].Notes != "UAT env\nBuild: 1.2\nBrowser: Chrome" || runs[0].Cycle != 1 {
		t.Fatalf("uat runs = %+v", runs)
	}
	items, _ := repo.ListQARunItems(ctx, e.s, runs[0].ID, repo.QAItemFilter{})
	statuses := map[string]string{}
	for _, it := range items {
		statuses[it.Title] = it.Status
	}
	if len(items) != 3 || statuses["Login valid"] != "passed" || statuses[`Login <b>invalid</b> "é" 🚀`] != "blocked" ||
		statuses["Deleted but referenced"] != "failed" {
		t.Fatalf("items = %+v", items)
	}
	if items[0].GroupName != "Auth / Login" {
		t.Fatalf("group name = %q; want the section path", items[0].GroupName)
	}
	admin, _ := global.UserByEmail(ctx, e.db, "admin@javan.co.id")
	for _, it := range items {
		if it.Title == "Deleted but referenced" {
			if it.ExecutedBy == nil || *it.ExecutedBy != admin.ID || it.StatusAt == nil {
				t.Fatalf("failed item executor/time = %v %v; want the alias-merged admin", it.ExecutedBy, it.StatusAt)
			}
			hist, _ := repo.ListQAResults(ctx, e.s, it.ID)
			if len(hist) != 1 || hist[0].Comment != "500 on submit" {
				t.Fatalf("history = %+v", hist)
			}
		}
	}
	counts, _ := repo.QACountsForProject(ctx, e.s, p.ID)
	if counts.Runs != 3 || counts.Items != 5 || counts.Results != 2 ||
		counts.ItemStatus["skipped"] != 1 || counts.ItemStatus["untested"] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestImport_IsIdempotentAndResumable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.run(t)
	p := e.portal(t)
	before, _ := repo.QACountsForProject(ctx, e.s, p.ID)
	rep := e.run(t) // second run: everything already there
	after, _ := repo.QACountsForProject(ctx, e.s, p.ID)
	if before.Items != after.Items || before.Cases != after.Cases || before.Results != after.Results {
		t.Fatalf("second run changed counts: %+v -> %+v", before, after)
	}
	if rep.Tables["projects"].SkipReasons["already imported"] != 1 {
		t.Fatalf("report = %+v", rep.Tables["projects"])
	}
	// A project is written in one transaction whose last write is its done
	// marker, so it is either fully present with exactly one marker or absent.
	// (The kill-and-resume behaviour on real data is checked in Task 19.)
	var markers int
	withOrg(t, e, func(tx *sqlx.Tx) {
		tx.Get(&markers, `SELECT count(*) FROM qa_legacy_ids WHERE legacy_table = 'project_done'`)
	})
	if markers != 1 {
		t.Fatalf("done markers = %d; want 1", markers)
	}
}

func TestImport_ConcurrentRunsDoNotDuplicate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.run(t) // users exist, so the two runs below only race on projects
	withOrg(t, e, func(tx *sqlx.Tx) {
		tx.Exec(`DELETE FROM qa_legacy_ids WHERE legacy_table = 'project_done'`)
		tx.Exec(`DELETE FROM projects`)
	})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := qaimport.New(e.db, e.legacy, e.store, e.opt).Run(ctx)
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}
	ps, _ := repo.ListProjects(ctx, e.s, true)
	if len(ps) != 1 {
		t.Fatalf("projects after two concurrent runs = %d; want 1", len(ps))
	}
}

func TestImport_DryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.opt.DryRun = true
	rep := e.run(t)
	if rep.ProjectsImported != 1 || rep.Tables["cases"].Imported != 4 ||
		rep.Tables["users"].Imported != 4 || rep.Tables["memberships"].Imported != 3 {
		t.Fatalf("dry-run report = %+v", rep)
	}
	if r, err := e.store.Open(context.Background(), "qa/"+e.orgID.String()+"/legacy/"+lid("f1").String()); err == nil {
		r.Close()
		t.Fatal("dry run copied an attachment into the blob store")
	}
	ps, _ := repo.ListProjects(context.Background(), e.s, true)
	if len(ps) != 0 {
		t.Fatalf("dry run created projects: %+v", ps)
	}
	if u, _ := global.UserByEmail(context.Background(), e.db, "admin@javan.co.id"); u != nil {
		t.Fatal("dry run created users")
	}
}

func TestImport_ClosesIdlePlans(t *testing.T) {
	e := newEnv(t)
	e.opt.CloseIdleBefore = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	e.run(t)
	p := e.portal(t)
	plans, _ := repo.ListQAPlans(context.Background(), e.s, p.ID)
	for _, pl := range plans {
		if pl.ClosedAt == nil {
			t.Errorf("plan %q still open", pl.Name)
		}
		runs, _ := repo.ListQARuns(context.Background(), e.s, pl.ID)
		for _, r := range runs {
			if r.ClosedAt == nil {
				t.Errorf("run %q still open", r.Name)
			}
		}
	}
}

func withOrg(t *testing.T, e env, fn func(tx *sqlx.Tx)) {
	t.Helper()
	if err := e.s.Tx(context.Background(), func(tx *sqlx.Tx) error { fn(tx); return nil }); err != nil {
		t.Fatal(err)
	}
}
