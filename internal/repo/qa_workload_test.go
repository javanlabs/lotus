package repo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// wlUser adds a user to the scope's organization with roleKey and status.
func wlUser(t *testing.T, db *sqlx.DB, s tenant.Scope, email, name, roleKey, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Get(&id, `INSERT INTO users (email, name, email_verified_at) VALUES ($1, $2, now()) RETURNING id`, email, name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memberships (org_id, user_id, role_id, status)
		SELECT $1, $2, id, $4 FROM roles WHERE key = $3 AND org_id IS NULL`, s.OrgID(), id, roleKey, status); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return id
}

// wlExec runs a statement in the scope's tenant transaction — for moving
// created_at into the month under test.
func wlExec(t *testing.T, s tenant.Scope, q string, args ...any) {
	t.Helper()
	if err := s.Tx(context.Background(), func(tx *sqlx.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// wlRun creates a run of the plan, dated at (a timestamp in the database's
// zone), and returns it with its items by case title.
func wlRun(t *testing.T, s tenant.Scope, planID uuid.UUID, name, at string) (*repo.QARun, map[string]uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	run, err := repo.CreateQARun(ctx, s, planID, repo.QARunInput{Name: name, Distribution: repo.DistributeNone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wlExec(t, s, `UPDATE qa_runs SET created_at = $2::timestamp WHERE id = $1`, run.ID, at)
	items, err := repo.ListQARunItems(ctx, s, run.ID, repo.QAItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uuid.UUID{}
	for _, it := range items {
		out[it.Title] = it.ID
	}
	return run, out
}

func wlItem(t *testing.T, s tenant.Scope, itemID uuid.UUID, assignee *uuid.UUID, status string) {
	t.Helper()
	wlExec(t, s, `UPDATE qa_run_items SET assignee_id = $2, status = $3 WHERE id = $1`, itemID, assignee, status)
}

func wlCase(t *testing.T, s tenant.Scope, projectID, sectionID uuid.UUID, title string, by *uuid.UUID, at string) {
	t.Helper()
	c, err := repo.CreateQACase(context.Background(), s, projectID, repo.QACaseInput{SectionID: sectionID, Title: title}, by)
	if err != nil {
		t.Fatal(err)
	}
	wlExec(t, s, `UPDATE qa_cases SET created_at = $2::timestamp WHERE id = $1`, c.ID, at)
}

func wlIssue(t *testing.T, s tenant.Scope, projectID uuid.UUID, title string, by *uuid.UUID, at string) *repo.QAIssue {
	t.Helper()
	is, err := repo.CreateQAIssue(context.Background(), s, projectID, repo.QAIssueInput{Title: title, Severity: "major", Source: "internal"}, by, nil)
	if err != nil {
		t.Fatal(err)
	}
	wlExec(t, s, `UPDATE qa_issues SET created_at = $2::timestamp WHERE id = $1`, is.ID, at)
	return is
}

// wlProject is a second project with one case in one scenario in one plan.
func wlProject(t *testing.T, s tenant.Scope, name string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	p := mustProject(t, s, name, nil)
	sec, _ := repo.CreateQASection(ctx, s, p.ID, "All", nil)
	c := mustQACase(t, s, p.ID, sec.ID, "Pay invoice")
	sc, _ := repo.CreateQAScenario(ctx, s, p.ID, "Smoke", "smoke", "", nil)
	repo.AddQAScenarioCases(ctx, s, sc.ID, nil, []uuid.UUID{c.ID})
	plan, err := repo.CreateQAPlan(ctx, s, p.ID, repo.QAPlanInput{Name: "Smoke", Phase: "smoke", ScenarioIDs: []uuid.UUID{sc.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID, plan.ID
}

func TestQAWorkload_MonthRecapPerPersonAndPerProject(t *testing.T) {
	f := newRunFixture(t, "acme-workload")
	ctx := context.Background()
	owner := wlUser(t, f.db, f.s, "owner@wl.example", "Olga Owner", "owner", "active")
	admin := wlUser(t, f.db, f.s, "admin@wl.example", "Adi Admin", "admin", "active")
	member := wlUser(t, f.db, f.s, "member@wl.example", "", "member", "active")
	guest := wlUser(t, f.db, f.s, "guest@wl.example", "Gita Guest", "guest", "active")
	wlUser(t, f.db, f.s, "viewer@wl.example", "Vino Viewer", "viewer", "active")
	suspended := wlUser(t, f.db, f.s, "gone@wl.example", "Sam Suspended", "member", "suspended")

	// Cases: on both edges of March.
	sec := f.login.SectionID
	wlCase(t, f.s, f.projectID, sec, "First of March", &admin, "2026-03-01 00:00:00")
	wlCase(t, f.s, f.projectID, sec, "Last of February", &admin, "2026-02-28 23:59:59")
	wlCase(t, f.s, f.projectID, sec, "Last of March", &member, "2026-03-31 23:59:59")
	wlCase(t, f.s, f.projectID, sec, "By the suspended", &suspended, "2026-03-15 12:00:00")

	// Portal: one run in March (items to the admin and the guest, one
	// unassigned) and one on the first of April.
	march, items := wlRun(t, f.s, f.plan.ID, "Cycle 1", "2026-03-10 09:00:00")
	wlItem(t, f.s, items["Login"], &admin, "passed")
	wlItem(t, f.s, items["Report"], &admin, "failed")
	wlItem(t, f.s, items["Create RFS"], &guest, "untested")
	_, april := wlRun(t, f.s, f.plan.ID, "Cycle 2", "2026-04-01 00:00:00")
	for _, id := range april {
		wlItem(t, f.s, id, &admin, "passed")
	}
	// Billing: the admin's second project this month.
	billing, billingPlan := wlProject(t, f.s, "billing")
	bRun, bItems := wlRun(t, f.s, billingPlan, "Smoke 1", "2026-03-20 15:00:00")
	wlItem(t, f.s, bItems["Pay invoice"], &admin, "blocked")

	// Issues.
	adminBug := wlIssue(t, f.s, f.projectID, "Login button misaligned", &admin, "2026-03-05 10:00:00")
	wlIssue(t, f.s, f.projectID, "Old bug", &member, "2026-02-27 10:00:00")
	wlIssue(t, f.s, f.projectID, "Client found it", &guest, "2026-03-06 10:00:00")

	// Another organization's March activity stays out.
	other := newRunFixture(t, "acme-workload-other")
	stranger := wlUser(t, other.db, other.s, "stranger@wl.example", "Stranger", "admin", "active")
	_, oItems := wlRun(t, other.s, other.plan.ID, "Theirs", "2026-03-10 09:00:00")
	for _, id := range oItems {
		wlItem(t, other.s, id, &stranger, "failed")
	}
	wlIssue(t, other.s, other.projectID, "Theirs", &stranger, "2026-03-10 10:00:00")

	w, err := repo.LoadQAWorkload(ctx, f.s, "2026-03")
	if err != nil {
		t.Fatal(err)
	}

	// People: owners, admins and members who are active — no guest,
	// viewer, suspended member or stranger.
	people := map[uuid.UUID]repo.QAWorkloadPerson{}
	for _, p := range w.People {
		people[p.UserID] = p
	}
	if len(people) != 3 {
		t.Fatalf("people = %+v; want the owner, the admin and the member", w.People)
	}
	if _, ok := people[owner]; !ok {
		t.Fatalf("owner missing: %+v", w.People)
	}
	a := people[admin]
	if a.Name != "Adi Admin" || a.Role != "admin" || a.Projects != 2 || a.CasesCreated != 1 || a.IssuesReported != 1 ||
		a.Total != 3 || a.Passed != 1 || a.Failed != 1 || a.Blocked != 1 || a.Untested != 0 || a.Skipped != 0 {
		t.Fatalf("admin = %+v", a)
	}
	m := people[member]
	if m.Name != "member@wl.example" || m.CasesCreated != 1 || m.IssuesReported != 0 || m.Total != 0 || m.Projects != 0 {
		t.Fatalf("member = %+v; want one case, named by email", m)
	}
	if o := people[owner]; o.CasesCreated != 0 || o.Total != 0 || o.IssuesReported != 0 {
		t.Fatalf("owner = %+v; want nothing", o)
	}

	// Projects: only those with activity in March, everyone's work counted.
	projects := map[uuid.UUID]repo.QAWorkloadProject{}
	for _, p := range w.Projects {
		projects[p.ProjectID] = p
	}
	if len(projects) != 2 {
		t.Fatalf("projects = %+v; want portal and billing", w.Projects)
	}
	pp := projects[f.projectID]
	if pp.Name != "portal" || pp.CasesCreated != 3 || pp.RunsCreated != 1 || pp.IssuesCreated != 2 ||
		pp.Total != 3 || pp.Passed != 1 || pp.Failed != 1 || pp.Untested != 1 {
		t.Fatalf("portal = %+v", pp)
	}
	if b := projects[billing]; b.CasesCreated != 0 || b.RunsCreated != 1 || b.Total != 1 || b.Blocked != 1 || b.IssuesCreated != 0 {
		t.Fatalf("billing = %+v", b)
	}

	// Runs: March's two, with totals; per person, only assigned items.
	if len(w.Runs) != 2 {
		t.Fatalf("runs = %+v; want Cycle 1 and Smoke 1", w.Runs)
	}
	for _, r := range w.Runs {
		switch r.RunID {
		case march.ID:
			if r.Name != "Cycle 1" || r.ProjectID != f.projectID || r.Total != 3 || r.AssigneeID != nil {
				t.Fatalf("march run = %+v", r)
			}
		case bRun.ID:
			if r.ProjectName != "billing" || r.Total != 1 {
				t.Fatalf("billing run = %+v", r)
			}
		default:
			t.Fatalf("unexpected run %+v", r)
		}
	}
	var adminRuns, guestRuns int
	for _, r := range w.PersonRuns {
		if r.AssigneeID == nil {
			t.Fatalf("person run without an assignee: %+v", r)
		}
		switch *r.AssigneeID {
		case admin:
			adminRuns++
			if r.RunID == march.ID && (r.Total != 2 || r.Passed != 1 || r.Failed != 1) {
				t.Fatalf("admin's march run = %+v", r)
			}
		case guest:
			guestRuns++
		}
	}
	if adminRuns != 2 || guestRuns != 1 {
		t.Fatalf("person runs = %+v", w.PersonRuns)
	}

	// Issues: March's two, with reporter names; not February's, not theirs.
	if len(w.Issues) != 2 {
		t.Fatalf("issues = %+v", w.Issues)
	}
	for _, is := range w.Issues {
		if is.IssueID == adminBug.ID && (is.Reporter != "Adi Admin" || is.Key != adminBug.Key || is.ProjectName != "portal" || is.ReportedBy == nil || *is.ReportedBy != admin) {
			t.Fatalf("admin's issue = %+v", is)
		}
	}
}

func TestQAWorkload_RejectsAMalformedMonth(t *testing.T) {
	f := newRunFixture(t, "acme-workload-bad")
	for _, m := range []string{"", "2026-13", "2026-3", "march", "2026-03-01"} {
		if _, err := repo.LoadQAWorkload(context.Background(), f.s, m); err != repo.ErrInvalidInput {
			t.Errorf("month %q: err = %v; want ErrInvalidInput", m, err)
		}
	}
}

func TestQAWorkload_CurrentMonthIsTheDatabases(t *testing.T) {
	f := newRunFixture(t, "acme-workload-now")
	var want string
	if err := f.db.Get(&want, `SELECT to_char(now(), 'YYYY-MM')`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.CurrentQAMonth(context.Background(), f.s)
	if err != nil || got != want {
		t.Fatalf("current month = %q, %v; want %q", got, err, want)
	}
}
