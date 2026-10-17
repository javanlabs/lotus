package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/uyab/lotus/internal/repo"
)

func TestQAWorkload_OwnersAndAdminsOnly(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-workload-web")
	admin, _ := e.session(t, "admin@wl.example", "admin")
	member, _ := e.session(t, "member@wl.example", "member")
	viewer, _ := e.session(t, "viewer@wl.example", "viewer")
	guest, _ := e.session(t, "guest@wl.example", "guest")

	for name, raw := range map[string]string{"owner": e.owner, "admin": admin} {
		if status, body := e.get(t, raw, "/qa/workload"); status != http.StatusOK || !strings.Contains(body, "QC recap") {
			t.Errorf("%s: status %d", name, status)
		}
		// The QA overview links to it.
		if _, body := e.get(t, raw, "/qa"); !strings.Contains(body, `href="/qa/workload"`) {
			t.Errorf("%s: /qa does not link to the workload page", name)
		}
	}
	for name, raw := range map[string]string{"member": member, "viewer": viewer, "guest": guest} {
		if status, _ := e.get(t, raw, "/qa/workload"); status != http.StatusForbidden {
			t.Errorf("%s: status %d; want 403", name, status)
		}
	}
	if _, body := e.get(t, member, "/qa"); strings.Contains(body, `href="/qa/workload"`) {
		t.Error("a member's /qa links to the workload page")
	}
	if _, body := e.get(t, member, "/qa/my"); strings.Contains(body, `href="/qa/workload"`) {
		t.Error("a member's /qa/my links to the workload page")
	}
}

func TestQAWorkload_ShowsTheMonthsPeopleAndProjects(t *testing.T) {
	t.Parallel()
	e := newQAEnv(t, "acme-workload-month")
	ctx := context.Background()
	_, adminID := e.session(t, "adi@wl.example", "admin")
	e.db.MustExec(`UPDATE users SET name = 'Adi <Admin>' WHERE id = $1`, adminID)
	portal := e.project(t, "Portal")
	e.project(t, "Quiet")
	sec, _ := repo.CreateQASection(ctx, e.s, portal, "Auth", nil)
	c, err := repo.CreateQACase(ctx, e.s, portal, repo.QACaseInput{SectionID: sec.ID, Title: "Login"}, &adminID)
	if err != nil {
		t.Fatal(err)
	}
	is, err := repo.CreateQAIssue(ctx, e.s, portal, repo.QAIssueInput{Title: "Broken login", Severity: "major", Source: "internal"}, &adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.Tx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.Exec(`UPDATE qa_cases SET created_at = '2026-03-04 10:00' WHERE id = $1`, c.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE qa_issues SET created_at = '2026-03-05 10:00' WHERE id = $1`, is.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	status, body := e.get(t, e.owner, "/qa/workload?month=2026-03")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "March 2026") || !strings.Contains(body, `href="/qa/workload?month=2026-02"`) || !strings.Contains(body, `href="/qa/workload?month=2026-04"`) {
		t.Fatalf("month header or prev/next missing:\n%s", body)
	}
	// Rows travel as JSON, escaped for the script context.
	if !strings.Contains(body, `Adi \u003cAdmin\u003e`) || strings.Contains(body, "Adi <Admin>") {
		t.Fatalf("admin row missing or unescaped:\n%s", body)
	}
	if !strings.Contains(body, `"key":"BUG-`+strconv.Itoa(is.Key)+`"`) || !strings.Contains(body, `"name":"Portal"`) || strings.Contains(body, `"name":"Quiet"`) {
		t.Fatalf("project rows wrong:\n%s", body)
	}

	// Another month has none of it; a malformed one falls back to this month.
	if _, body := e.get(t, e.owner, "/qa/workload?month=2026-02"); strings.Contains(body, `"name":"Portal"`) {
		t.Fatal("February shows March's project")
	}
	status, body = e.get(t, e.owner, "/qa/workload?month=nope")
	if status != http.StatusOK || !strings.Contains(body, "Unknown month") {
		t.Fatalf("malformed month = %d:\n%s", status, body)
	}
}
